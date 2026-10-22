package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	"github.com/kirsle/configdir"
	"gotest.tools/v3/assert"
)

type doctorVersionHandler struct{ protocol int }

func (h doctorVersionHandler) HandleCommand(req common.Request) common.Response {
	if req.Cmd == common.CmdVersion {
		return common.OKResponse(common.VersionResult{Protocol: h.protocol, Version: "dev"})
	}
	return common.ErrResponse(common.CodeUnknownCmd, "unknown command: "+req.Cmd)
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
}

func setupDoctorHome(t *testing.T) (string, doctorEnv) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "gas-doc")
	assert.NilError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })
	home, err = filepath.EvalSymlinks(home)
	assert.NilError(t, err)

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitconfig := filepath.Join(home, ".gitconfig")
	assert.NilError(t, os.WriteFile(gitconfig, []byte("[user]\n\tname = Doc Test\n\temail = doc@example.com\n[init]\n\tdefaultBranch = main\n"), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	configdir.Refresh()

	env := newDoctorEnv()
	env.home = home
	env.goos = "darwin"
	env.checkXcode = false
	env.socketPath = filepath.Join(home, "d.sock")
	env.stateDir = home
	env.executable = ""
	env.cmdTimeout = 10 * time.Second
	env.remoteTimeout = 10 * time.Second
	return home, env
}

func writeDoctorConfig(t *testing.T, env doctorEnv, repos ...string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Dir(env.configPath), 0o755))
	data, err := json.Marshal(map[string]interface{}{"repos": repos, "envs": []string{}})
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(env.configPath, data, 0o644))
}

func findCheck(t *testing.T, report DoctorReport, repo, name string) DoctorCheck {
	t.Helper()
	for _, c := range report.Checks {
		if c.Repo == repo && c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check for repo %q in %+v", name, repo, report.Checks)
	return DoctorCheck{}
}

func TestDoctorReportsProblems(t *testing.T) {
	home, env := setupDoctorHome(t)

	remote := filepath.Join(home, "remote.git")
	runGitT(t, home, "init", "-q", "--bare", remote)
	good := filepath.Join(home, "good")
	runGitT(t, home, "clone", "-q", remote, good)
	assert.NilError(t, os.WriteFile(filepath.Join(good, "a.txt"), []byte("a"), 0o644))
	runGitT(t, good, "add", "a.txt")
	runGitT(t, good, "commit", "-qm", "init")
	runGitT(t, good, "push", "-q", "origin", "main")

	noUpstream := filepath.Join(home, "noup")
	runGitT(t, home, "init", "-q", noUpstream)
	runGitT(t, noUpstream, "commit", "-q", "--allow-empty", "-m", "x")

	gone := filepath.Join(home, "gone")
	notRepo := filepath.Join(home, "plain")
	assert.NilError(t, os.Mkdir(notRepo, 0o755))

	deleted := filepath.Join(home, "deleted")
	runGitT(t, home, "clone", "-q", remote, deleted)
	runGitT(t, deleted, "switch", "-q", "-c", "feature")
	runGitT(t, deleted, "push", "-q", "-u", "origin", "feature")
	runGitT(t, remote, "branch", "-q", "-D", "feature")

	sub := filepath.Join(good, "sub")
	assert.NilError(t, os.Mkdir(sub, 0o755))

	writeDoctorConfig(t, env, good, noUpstream, gone, notRepo, deleted, sub)

	agents := filepath.Join(home, "Library", "LaunchAgents")
	assert.NilError(t, os.MkdirAll(agents, 0o755))
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ProgramArguments</key><array><string>/nonexistent/git-auto-sync-daemon</string></array>
</dict></plist>`
	assert.NilError(t, os.WriteFile(filepath.Join(agents, "git-auto-sync-daemon.plist"), []byte(plist), 0o644))

	assert.NilError(t, os.WriteFile(filepath.Join(home, "git-auto-sync-daemon.err.log"), bytes.Repeat([]byte("x"), 2048), 0o644))

	report := runDoctor(env)
	assert.Assert(t, !report.OK)

	assert.Equal(t, findCheck(t, report, "", "git").Status, checkOK)
	assert.Equal(t, findCheck(t, report, "", "config").Status, checkOK)
	assert.Equal(t, findCheck(t, report, good, "repository").Status, checkOK)
	assert.Equal(t, findCheck(t, report, good, "author").Detail, "Doc Test <doc@example.com>")
	assert.Equal(t, findCheck(t, report, good, "upstream").Status, checkOK)
	assert.Equal(t, findCheck(t, report, good, "remote").Status, checkOK)

	up := findCheck(t, report, noUpstream, "upstream")
	assert.Equal(t, up.Status, checkFail)
	assert.Assert(t, strings.Contains(up.Fix, "branch -u origin/main"), up.Fix)

	assert.Equal(t, findCheck(t, report, gone, "folder").Status, checkFail)
	assert.Equal(t, findCheck(t, report, notRepo, "repository").Status, checkFail)

	del := findCheck(t, report, deleted, "remote")
	assert.Equal(t, del.Status, checkFail)
	assert.Assert(t, strings.Contains(del.Detail, "feature no longer exists"), del.Detail)

	assert.Equal(t, findCheck(t, report, sub, "repository").Status, checkWarn)

	daemon := findCheck(t, report, "", "daemon")
	assert.Equal(t, daemon.Status, checkFail)
	assert.Equal(t, daemon.Fix, "git-auto-sync daemon start")

	agent := findCheck(t, report, "", "LaunchAgent")
	assert.Equal(t, agent.Status, checkFail)
	assert.Assert(t, strings.Contains(agent.Detail, "/nonexistent/git-auto-sync-daemon"))

	old := findCheck(t, report, "", "old logs")
	assert.Equal(t, old.Status, checkWarn)
	assert.Assert(t, strings.Contains(old.Detail, "2.0 KB"), old.Detail)

	assert.Equal(t, findCheck(t, report, "", "log folder").Status, checkOK)

	for _, c := range report.Checks {
		if c.Name == "gh" {
			t.Fatalf("gh must only be checked when a repo uses GitHub: %+v", c)
		}
	}

	var out bytes.Buffer
	printDoctorReport(&out, report)
	text := out.String()
	assert.Assert(t, strings.Contains(text, "✗ upstream: branch 'main' has no upstream"), text)
	assert.Assert(t, strings.Contains(text, "fix: git -C "+noUpstream+" branch -u origin/main"), text)
	assert.Assert(t, strings.Contains(text, "⚠ old logs"), text)
	assert.Assert(t, strings.Index(text, "System") < strings.Index(text, "Daemon"), text)

	_, err := os.Stat(filepath.Dir(env.logPath))
	assert.Assert(t, os.IsNotExist(err), "doctor must not create the log folder")
	_, err = os.Stat(gone)
	assert.Assert(t, os.IsNotExist(err))
}

func TestDoctorDaemonStates(t *testing.T) {
	_, env := setupDoctorHome(t)

	report := runDoctor(env)
	assert.Equal(t, findCheck(t, report, "", "config").Status, checkWarn)
	assert.Equal(t, findCheck(t, report, "", "daemon").Status, checkWarn)
	agent := findCheck(t, report, "", "LaunchAgent")
	assert.Equal(t, agent.Status, checkWarn)
	assert.Equal(t, agent.Fix, "git-auto-sync daemon enable")
	assert.Equal(t, findCheck(t, report, "", "daemon").Fix, "git-auto-sync daemon enable")

	srv, err := common.NewSocketServer(env.socketPath, doctorVersionHandler{protocol: common.ProtocolVersion})
	assert.NilError(t, err)
	go srv.Accept()
	report = runDoctor(env)
	srv.Close()
	assert.Equal(t, findCheck(t, report, "", "daemon").Status, checkOK)
	perms := findCheck(t, report, "", "socket permissions")
	assert.Equal(t, perms.Status, checkOK, perms.Detail)

	srv, err = common.NewSocketServer(env.socketPath, doctorVersionHandler{protocol: 1})
	assert.NilError(t, err)
	go srv.Accept()
	report = runDoctor(env)
	srv.Close()
	assert.Equal(t, findCheck(t, report, "", "daemon").Status, checkFail)

	_ = os.Remove(env.socketPath)
	ln, err := net.Listen("unix", env.socketPath)
	assert.NilError(t, err)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte(`{"type":"hello","version":"old","repos":[]}` + "\n"))
			time.Sleep(100 * time.Millisecond)
			conn.Close()
		}
	}()
	report = runDoctor(env)
	old := findCheck(t, report, "", "daemon")
	assert.Equal(t, old.Status, checkFail)
	assert.Assert(t, strings.HasSuffix(old.Fix, "git-auto-sync daemon restart"), old.Fix)
	assert.Equal(t, findCheck(t, report, "", "socket permissions").Status, checkWarn)
}

func TestDoctorRebaseInProgress(t *testing.T) {
	home, env := setupDoctorHome(t)
	repo := filepath.Join(home, "rebasing")
	runGitT(t, home, "init", "-q", repo)
	runGitT(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	runGitT(t, repo, "switch", "-q", "-c", "other")
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "c.txt"), []byte("other"), 0o644))
	runGitT(t, repo, "add", "c.txt")
	runGitT(t, repo, "commit", "-qm", "other")
	runGitT(t, repo, "switch", "-q", "main")
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "c.txt"), []byte("mine"), 0o644))
	runGitT(t, repo, "add", "c.txt")
	runGitT(t, repo, "commit", "-qm", "mine")
	cmd := exec.Command("git", "rebase", "other")
	cmd.Dir = repo
	assert.Assert(t, cmd.Run() != nil, "rebase should stop on a conflict")
	writeDoctorConfig(t, env, repo)

	report := runDoctor(env)
	state := findCheck(t, report, repo, "state")
	assert.Equal(t, state.Status, checkFail)
	assert.Assert(t, strings.Contains(state.Fix, "rebase --abort"), state.Fix)
	for _, c := range report.Checks {
		if c.Repo == repo && (c.Name == "branch" || c.Name == "upstream") {
			t.Fatalf("mid-rebase repo must not get branch advice: %+v", c)
		}
	}
}

func TestDoctorLongSocketPath(t *testing.T) {
	_, env := setupDoctorHome(t)
	env.socketPath = "/tmp/" + strings.Repeat("x", 120) + "/d.sock"
	report := runDoctor(env)
	assert.Equal(t, findCheck(t, report, "", "socket path").Status, checkFail)
}

func TestPlistStrings(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>EnvironmentVariables</key>
	<dict><key>PATH</key><string>/usr/bin</string></dict>
	<key>KeepAlive</key>
	<true/>
	<key>Label</key>
	<string>git-auto-sync-daemon</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/git-auto-sync-daemon</string>
		<string>--flag</string>
	</array>
	<key>StandardErrorPath</key>
	<string>/Users/x/git-auto-sync-daemon.err.log</string>
</dict>
</plist>`)
	values, err := plistStrings(data)
	assert.NilError(t, err)
	assert.Equal(t, values["ProgramArguments"], "/usr/local/bin/git-auto-sync-daemon")
	assert.Equal(t, values["Label"], "git-auto-sync-daemon")
	assert.Equal(t, values["StandardErrorPath"], "/Users/x/git-auto-sync-daemon.err.log")
	_, hasPath := values["PATH"]
	assert.Assert(t, !hasPath)
	assert.Equal(t, values["EnvironmentVariables"], "")

	_, err = plistStrings([]byte("<plist><dict><key>x</string>"))
	assert.Assert(t, err != nil)
}

func TestDoctorHelpers(t *testing.T) {
	assert.Equal(t, shellQuote("/a/b-c_d.e"), "/a/b-c_d.e")
	assert.Equal(t, shellQuote("/a/b c"), "'/a/b c'")
	assert.Equal(t, shellQuote("it's"), `'it'\''s'`)
	assert.Equal(t, redactURL("https://user:tok@github.com/a/b.git"), "https://***@github.com/a/b.git")
	assert.Equal(t, redactURL("https://github.com/a/b@c.git"), "https://github.com/a/b@c.git")
	assert.Equal(t, redactURL("git@github.com:a/b.git"), "git@github.com:a/b.git")
	assert.Equal(t, humanSize(15*1024*1024), "15.0 MB")
	assert.Equal(t, gitErrorLine("remote: x\nfatal: Authentication failed for 'u'\n"), "Authentication failed for 'u'")
}
