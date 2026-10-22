package common

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func envValue(env []string, key string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == key {
			val, found = v, true
		}
	}
	return val, found
}

func Test_ToEnvString(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("GIT_TRACE", "0")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	env := toEnvString(RepoConfig{Env: []string{"FOO=bar"}})

	path, _ := envValue(env, "PATH")
	if runtime.GOOS != "windows" {
		assert.Assert(t, strings.Contains(path, "/opt/homebrew/bin"))
		assert.Assert(t, strings.Contains(path, "/usr/local/bin"))
		assert.Assert(t, strings.HasPrefix(path, "/usr/bin:/bin"))
	}
	for key, want := range map[string]string{
		"SSH_AUTH_SOCK":       "/tmp/agent.sock",
		"LANG":                "en_US.UTF-8",
		"GIT_TRACE":           "0",
		"FOO":                 "bar",
		"GIT_TERMINAL_PROMPT": "0",
		"LC_MESSAGES":         "C",
	} {
		got, ok := envValue(env, key)
		assert.Assert(t, ok, key)
		assert.Equal(t, got, want, key)
	}
	_, hasGitDir := envValue(env, "GIT_DIR")
	assert.Assert(t, !hasGitDir)
}

func Test_AugmentPathNoDuplicates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	assert.Equal(t, augmentPath("/opt/homebrew/bin:/usr/bin:/usr/local/bin"), "/opt/homebrew/bin:/usr/bin:/usr/local/bin")
}

func Test_GitTimeouts(t *testing.T) {
	assert.Equal(t, gitTimeout([]string{"fetch", "origin"}), gitNetworkTimeout)
	assert.Equal(t, gitTimeout([]string{"-c", "x=y", "push", "origin"}), gitNetworkTimeout)
	assert.Equal(t, gitTimeout([]string{"--literal-pathspecs", "reset", "-q"}), gitDefaultTimeout)
	assert.Equal(t, gitTimeout([]string{"rebase", "origin/main"}), gitWriteTimeout)
	assert.Equal(t, gitTimeout([]string{"add", "-A"}), gitWriteTimeout)
	assert.Equal(t, gitTimeout([]string{"status"}), gitDefaultTimeout)
}

func Test_GitCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-git")
	assert.NilError(t, os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 10\n"), 0755))

	orig := gitDefaultTimeout
	gitDefaultTimeout = 200 * time.Millisecond
	defer func() { gitDefaultTimeout = orig }()

	start := time.Now()
	_, err := GitCommand(RepoConfig{RepoPath: dir, GitExec: script}, []string{"status"})
	assert.Assert(t, err != nil)
	assert.Equal(t, ClassifyError(err), CodeTimeout)
	assert.Assert(t, time.Since(start) < 5*time.Second)
}

func Test_LockPathFromStderr(t *testing.T) {
	assert.Equal(t, lockPathFromStderr("fatal: Unable to create '/r/.git/worktrees/w/index.lock': File exists.", "/r"), "/r/.git/worktrees/w/index.lock")
	assert.Equal(t, lockPathFromStderr("weird", "/r"), filepath.Join("/r", ".git", "index.lock"))
}
