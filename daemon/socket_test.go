package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GitJournal/git-auto-sync/common"
	"github.com/kirsle/configdir"
	"gotest.tools/v3/assert"
)

func testDaemon(t testing.TB) *Daemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "gasd")
	assert.NilError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	d := &Daemon{
		watchers: map[string]*watcherHandle{},
		state:    common.NewRepoStateStore(),
	}
	sock, err := common.NewSocketServer(filepath.Join(dir, "d.sock"), d)
	assert.NilError(t, err)
	t.Cleanup(sock.Close)
	d.sock = sock
	d.state.Ensure("/b")
	d.state.Ensure("/a")
	return d
}

func decode(t *testing.T, resp common.Response, v interface{}) {
	t.Helper()
	data, err := json.Marshal(resp.Result)
	assert.NilError(t, err)
	assert.NilError(t, json.Unmarshal(data, v))
}

func TestHandleStatus(t *testing.T) {
	d := testDaemon(t)
	d.sock.Emit(common.NewEvent(common.EventCommit, "/a", "c", ""))

	resp := d.HandleCommand(common.Request{Cmd: common.CmdStatus})
	assert.Assert(t, resp.OK)
	var status common.StatusResult
	decode(t, resp, &status)
	assert.Equal(t, status.Protocol, common.ProtocolVersion)
	assert.Equal(t, status.Daemon, "running")
	assert.Equal(t, status.LastEventID, uint64(1))
	assert.Equal(t, len(status.Repos), 2)
	assert.Equal(t, status.Repos[0].Path, "/a")

	resp = d.HandleCommand(common.Request{Cmd: common.CmdVersion})
	var v common.VersionResult
	decode(t, resp, &v)
	assert.Equal(t, v.Protocol, common.ProtocolVersion)
}

func TestHandleEvents(t *testing.T) {
	d := testDaemon(t)
	for _, repo := range []string{"/a", "/b", "/a"} {
		d.sock.Emit(common.NewEvent(common.EventPush, repo, "", ""))
	}

	resp := d.HandleCommand(common.Request{Cmd: common.CmdEvents, Since: 1, Repo: "/a/"})
	assert.Assert(t, resp.OK)
	var result common.EventsResult
	decode(t, resp, &result)
	assert.Equal(t, result.LastEventID, uint64(3))
	assert.Equal(t, len(result.Events), 1)
	assert.Equal(t, result.Events[0].ID, uint64(3))

	resp = d.HandleCommand(common.Request{Cmd: common.CmdEvents, Since: 3})
	data, err := json.Marshal(resp)
	assert.NilError(t, err)
	assert.Equal(t, string(data), `{"ok":true,"result":{"events":[],"lastEventId":3}}`)
}

func TestHandleErrors(t *testing.T) {
	d := testDaemon(t)

	cases := []struct {
		req  common.Request
		code string
	}{
		{common.Request{Cmd: "bogus"}, common.CodeUnknownCmd},
		{common.Request{Cmd: common.CmdSyncNow}, common.CodeMissingRepo},
		{common.Request{Cmd: common.CmdAdd, Repo: "relative/path"}, common.CodeBadRequest},
		{common.Request{Cmd: common.CmdSyncNow, Repo: "/not/watched"}, common.CodeNotWatching},
		{common.Request{Cmd: common.CmdSetSettings, Repo: "/a"}, common.CodeBadRequest},
	}
	for _, c := range cases {
		resp := d.HandleCommand(c.req)
		assert.Assert(t, !resp.OK, c.req.Cmd)
		assert.Equal(t, resp.Code, c.code, c.req.Cmd)
		assert.Assert(t, resp.Error != "", c.req.Cmd)
	}

	resp := d.HandleCommand(common.Request{Cmd: common.CmdPauseAll})
	assert.Assert(t, resp.OK)
}

func TestHandleEventsMatchesSymlinkedRepo(t *testing.T) {
	d := testDaemon(t)
	dir, err := os.MkdirTemp("", "gasl")
	assert.NilError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	real := filepath.Join(dir, "real")
	assert.NilError(t, os.Mkdir(real, 0o755))
	link := filepath.Join(dir, "link")
	assert.NilError(t, os.Symlink(real, link))
	resolved, err := filepath.EvalSymlinks(real)
	assert.NilError(t, err)

	d.sock.Emit(common.NewEvent(common.EventCommit, resolved, "", ""))
	d.sock.Emit(common.NewEvent(common.EventCommit, "/other", "", ""))

	resp := d.HandleCommand(common.Request{Cmd: common.CmdEvents, Repo: link})
	var result common.EventsResult
	decode(t, resp, &result)
	assert.Equal(t, len(result.Events), 1)
	assert.Equal(t, result.Events[0].Repo, resolved)
}

func FuzzHandleCommand(f *testing.F) {
	home, err := os.MkdirTemp("", "gasf")
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { os.RemoveAll(home) })
	f.Setenv("HOME", home)
	f.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	configdir.Refresh()

	d := testDaemon(f)
	for _, cmd := range []string{
		common.CmdStatus, common.CmdList, common.CmdAdd, common.CmdRemove, common.CmdSyncNow,
		common.CmdSyncAll, common.CmdPause, common.CmdResume, common.CmdPauseAll, common.CmdResumeAll,
		common.CmdGetSettings, common.CmdSetSettings, common.CmdAbortRebase, common.CmdEvents,
		common.CmdSubscribe, common.CmdVersion, "", "bogus",
	} {
		f.Add(cmd, "/a", false, uint64(0), 0, "pollInterval", "60")
	}
	f.Add(common.CmdEvents, "relative", true, uint64(1<<63), -1, "", "")
	f.Add(common.CmdSetSettings, "/nonexistent/\xff", false, uint64(0), 5, "bogus", "\x00")

	mutating := map[string]bool{
		common.CmdAdd: true, common.CmdRemove: true, common.CmdGetSettings: true,
		common.CmdSetSettings: true, common.CmdAbortRebase: true,
	}

	f.Fuzz(func(t *testing.T, cmd, repo string, force bool, since uint64, limit int, key, val string) {
		if mutating[cmd] {
			if _, err := os.Lstat(repo); err == nil {
				t.Skip("refusing to touch a real path")
			}
		}
		req := common.Request{Cmd: cmd, Repo: repo, Force: force, Since: since, Limit: limit}
		if key != "" {
			req.Settings = map[string]string{key: val}
		}
		resp := d.HandleCommand(req)
		if resp.OK == (resp.Code != "") {
			t.Fatalf("%+v: ok=%v code=%q", req, resp.OK, resp.Code)
		}
		if !resp.OK && resp.Error == "" {
			t.Fatalf("%+v: error without message", req)
		}
		if _, err := json.Marshal(resp); err != nil {
			t.Fatalf("%+v: marshal: %v", req, err)
		}
	})
}
