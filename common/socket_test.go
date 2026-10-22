package common

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

type echoHandler struct{}

func (echoHandler) HandleCommand(req Request) Response {
	switch req.Cmd {
	case CmdVersion:
		return OKResponse(VersionResult{Protocol: ProtocolVersion, Version: "test"})
	case "boom":
		panic("boom")
	}
	return ErrResponse(CodeUnknownCmd, "unknown command: "+req.Cmd)
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gas")
	assert.NilError(t, err)
	if len(dir) > 80 {
		os.RemoveAll(dir)
		dir, err = os.MkdirTemp("/tmp", "gas")
		assert.NilError(t, err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startTestServer(t *testing.T) *SocketServer {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "sock", "d.sock")
	srv, err := NewSocketServer(path, echoHandler{})
	assert.NilError(t, err)
	srv.SetHelloProvider(func() Hello {
		return Hello{Version: "test", Repos: []RepoState{{Path: "/r", Status: StatusOK}}}
	})
	go srv.Accept()
	t.Cleanup(srv.Close)
	return srv
}

type rawConn struct {
	t       *testing.T
	conn    net.Conn
	scanner *bufio.Scanner
}

func dialRaw(t *testing.T, srv *SocketServer) *rawConn {
	t.Helper()
	conn, err := net.Dial("unix", srv.Path())
	assert.NilError(t, err)
	t.Cleanup(func() { conn.Close() })
	return &rawConn{t: t, conn: conn, scanner: bufio.NewScanner(conn)}
}

func (c *rawConn) send(line string) {
	c.t.Helper()
	_, err := c.conn.Write([]byte(line + "\n"))
	assert.NilError(c.t, err)
}

func (c *rawConn) read(timeout time.Duration) (map[string]interface{}, bool) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	if !c.scanner.Scan() {
		return nil, false
	}
	var m map[string]interface{}
	assert.NilError(c.t, json.Unmarshal(c.scanner.Bytes(), &m))
	return m, true
}

func (c *rawConn) mustRead() map[string]interface{} {
	c.t.Helper()
	m, ok := c.read(2 * time.Second)
	assert.Assert(c.t, ok, "expected a frame")
	return m
}

func TestSocketRequestResponseEchoesID(t *testing.T) {
	srv := startTestServer(t)
	c := dialRaw(t, srv)

	c.send(`{"id":"a","cmd":"version"}`)
	c.send(`{"id":"b","cmd":"nope"}`)
	c.send(`{"cmd":"version"}`)
	c.send(`not json`)
	c.send(`{"id":"p","cmd":"boom"}`)

	a := c.mustRead()
	assert.Equal(t, a["id"], "a")
	assert.Equal(t, a["ok"], true)
	result := a["result"].(map[string]interface{})
	assert.Equal(t, result["protocol"], float64(ProtocolVersion))

	b := c.mustRead()
	assert.Equal(t, b["id"], "b")
	assert.Equal(t, b["ok"], false)
	assert.Equal(t, b["code"], CodeUnknownCmd)

	noID := c.mustRead()
	_, hasID := noID["id"]
	assert.Assert(t, !hasID)
	assert.Equal(t, noID["ok"], true)

	bad := c.mustRead()
	assert.Equal(t, bad["ok"], false)
	assert.Equal(t, bad["code"], CodeBadRequest)

	p := c.mustRead()
	assert.Equal(t, p["id"], "p")
	assert.Equal(t, p["ok"], false)
	assert.Equal(t, p["code"], CodeUnknown)

	srv.Emit(NewEvent(EventCommit, "/r", "x", ""))
	srv.BroadcastState(RepoState{Path: "/r", Status: StatusOK})
	_, ok := c.read(200 * time.Millisecond)
	assert.Assert(t, !ok, "non-subscribed connection must not receive push frames")
}

func TestSocketClientSendCommand(t *testing.T) {
	srv := startTestServer(t)

	resp, err := CallDaemon(srv.Path(), Request{Cmd: CmdVersion})
	assert.NilError(t, err)
	assert.Assert(t, resp.OK)
	var v VersionResult
	assert.NilError(t, resp.Decode(&v))
	assert.Equal(t, v.Protocol, ProtocolVersion)

	resp, err = CallDaemon(srv.Path(), Request{Cmd: "nope"})
	assert.NilError(t, err)
	assert.Equal(t, ErrorCode(resp.Err()), CodeUnknownCmd)
}

func TestSocketSubscribeOnlyPush(t *testing.T) {
	srv := startTestServer(t)
	srv.Emit(NewEvent(EventCommit, "/r", "before", ""))

	sub := dialRaw(t, srv)
	plain := dialRaw(t, srv)

	sub.send(`{"id":"s1","cmd":"subscribe"}`)
	resp := sub.mustRead()
	assert.Equal(t, resp["id"], "s1")
	assert.Equal(t, resp["ok"], true)

	hello := sub.mustRead()
	assert.Equal(t, hello["type"], FrameHello)
	assert.Equal(t, hello["protocol"], float64(ProtocolVersion))
	assert.Equal(t, hello["version"], "test")
	assert.Equal(t, hello["lastEventId"], float64(1))
	assert.Equal(t, len(hello["repos"].([]interface{})), 1)
	assert.Assert(t, srv.HasClients())

	srv.Emit(NewCodedEvent(EventOffline, "/r", "down", CodeNetwork))
	ev := sub.mustRead()
	assert.Equal(t, ev["type"], EventOffline)
	assert.Equal(t, ev["id"], float64(2))
	assert.Equal(t, ev["code"], CodeNetwork)

	srv.BroadcastState(RepoState{Path: "/r", Status: StatusOffline})
	st := sub.mustRead()
	assert.Equal(t, st["type"], FrameState)
	assert.Equal(t, st["repo"], "/r")
	assert.Assert(t, st["ts"].(float64) > 0)
	assert.Equal(t, st["data"].(map[string]interface{})["status"], StatusOffline)

	sub.send(`{"id":"v","cmd":"version"}`)
	v := sub.mustRead()
	assert.Equal(t, v["id"], "v")

	_, ok := plain.read(200 * time.Millisecond)
	assert.Assert(t, !ok, "plain connection received a push frame")
}

func TestSocketClientSubscribe(t *testing.T) {
	srv := startTestServer(t)
	client, err := NewSocketClient(srv.Path())
	assert.NilError(t, err)
	defer client.Close()

	hello, err := client.Subscribe()
	assert.NilError(t, err)
	assert.Equal(t, hello.Protocol, ProtocolVersion)

	srv.Emit(NewEvent(EventPush, "/r", "pushed", ""))
	frame, err := client.ReadFrame()
	assert.NilError(t, err)
	var ev Event
	assert.NilError(t, json.Unmarshal(frame, &ev))
	assert.Equal(t, ev.Type, EventPush)
	assert.Equal(t, ev.ID, uint64(1))
}

func TestSocketSlowClientDoesNotBlockBroadcast(t *testing.T) {
	srv := startTestServer(t)

	slow := dialRaw(t, srv)
	slow.send(`{"cmd":"subscribe"}`)
	slow.mustRead()
	slow.mustRead()
	assert.Assert(t, srv.HasClients())

	big := strings.Repeat("x", 16*1024)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		srv.Broadcast(map[string]string{"type": "state", "pad": big})
	}
	elapsed := time.Since(start)
	assert.Assert(t, elapsed < time.Second, "broadcast blocked for %s", elapsed)
	assert.Assert(t, !srv.HasClients(), "slow subscriber should have been dropped")

	buf := make([]byte, 64*1024)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = slow.conn.SetReadDeadline(time.Now().Add(time.Second))
		_, err := slow.conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("slow client connection was not closed")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow client connection was not closed")
		}
	}

	resp, err := CallDaemon(srv.Path(), Request{Cmd: CmdVersion})
	assert.NilError(t, err)
	assert.Assert(t, resp.OK)
}

func TestSocketPermissions(t *testing.T) {
	srv := startTestServer(t)
	info, err := os.Stat(srv.Path())
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0600))

	dirInfo, err := os.Stat(filepath.Dir(srv.Path()))
	assert.NilError(t, err)
	assert.Equal(t, dirInfo.Mode().Perm(), os.FileMode(0700))
}

func TestSocketRefusesLiveSocket(t *testing.T) {
	srv := startTestServer(t)
	_, err := NewSocketServer(srv.Path(), echoHandler{})
	assert.ErrorContains(t, err, "already listening")
}

func TestEventLogSince(t *testing.T) {
	l := NewMemoryEventLog(5)
	for i := 0; i < 8; i++ {
		repo := "/a"
		if i%2 == 1 {
			repo = "/b"
		}
		ev := l.Append(NewEvent(EventCommit, repo, "", ""))
		assert.Equal(t, ev.ID, uint64(i+1))
	}
	assert.Equal(t, l.LastID(), uint64(8))

	ids := func(evs []Event) []uint64 {
		out := []uint64{}
		for _, e := range evs {
			out = append(out, e.ID)
		}
		return out
	}

	assert.DeepEqual(t, ids(l.Since(0, "", 0)), []uint64{4, 5, 6, 7, 8})
	assert.DeepEqual(t, ids(l.Since(6, "", 0)), []uint64{7, 8})
	assert.DeepEqual(t, ids(l.Since(0, "", 2)), []uint64{7, 8})
	assert.DeepEqual(t, ids(l.Since(0, "/b", 0)), []uint64{4, 6, 8})
	assert.DeepEqual(t, ids(l.Since(8, "", 0)), []uint64{})
	assert.DeepEqual(t, ids(l.Since(100, "", 0)), []uint64{})
}

func TestEventLogPersistence(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "state", "events.jsonl")

	l, err := NewEventLog(path, 3)
	assert.NilError(t, err)
	for i := 0; i < 5; i++ {
		l.Append(NewEvent(EventPush, "/r", "", ""))
	}
	assert.NilError(t, l.Close())

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "\n"), 5)

	l2, err := NewEventLog(path, 3)
	assert.NilError(t, err)
	defer l2.Close()
	assert.Equal(t, l2.LastID(), uint64(5))
	evs := l2.Since(0, "", 0)
	assert.Equal(t, len(evs), 3)
	assert.Equal(t, evs[0].ID, uint64(3))

	data, err = os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "\n"), 3)

	ev := l2.Append(NewEvent(EventPush, "/r", "", ""))
	assert.Equal(t, ev.ID, uint64(6))

	for i := 0; i < 10; i++ {
		l2.Append(NewEvent(EventPush, "/r", "", ""))
	}
	data, err = os.ReadFile(path)
	assert.NilError(t, err)
	assert.Assert(t, strings.Count(string(data), "\n") <= 6)

	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0600))
}

func TestSocketEmitUsesEventLog(t *testing.T) {
	srv := startTestServer(t)
	path := filepath.Join(shortTempDir(t), "events.jsonl")
	l, err := NewEventLog(path, 10)
	assert.NilError(t, err)
	srv.SetEventLog(l)

	srv.Emit(NewEvent(EventCommit, "/r", "one", ""))
	srv.Emit(NewEvent(EventPush, "/r", "two", ""))
	evs := srv.Events().Since(1, "", 0)
	assert.Equal(t, len(evs), 1)
	assert.Equal(t, evs[0].Message, "two")
	assert.Assert(t, evs[0].Ts > 0)
}
