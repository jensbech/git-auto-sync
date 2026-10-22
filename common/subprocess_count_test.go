package common

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func countingGit(t *testing.T) (string, func() []string) {
	t.Helper()
	real, err := exec.LookPath("git")
	assert.NilError(t, err)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "git")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nexec '" + real + "' \"$@\"\n"
	assert.NilError(t, os.WriteFile(script, []byte(body), 0755))
	read := func() []string {
		data, _ := os.ReadFile(logPath)
		_ = os.Remove(logPath)
		var calls []string
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if l != "" {
				calls = append(calls, l)
			}
		}
		return calls
	}
	return script, read
}

func Test_SubprocessCounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script")
	}
	f := newSyncFixture(t)
	cfg, _ := f.config(f.a)
	script, calls := countingGit(t)
	cfg.GitExec = script

	assert.NilError(t, AutoSync(cfg))
	calls()

	assert.NilError(t, AutoSync(cfg))
	idle := calls()
	t.Logf("idle poll: %d git calls\n  %s", len(idle), strings.Join(idle, "\n  "))

	f.write(f.a, "edit.md", "edit\n")
	assert.NilError(t, AutoSync(cfg))
	edit := calls()
	t.Logf("one-file edit: %d git calls\n  %s", len(edit), strings.Join(edit, "\n  "))

	local := cfg
	local.LocalOnly = true
	f.write(f.a, "local.md", "local\n")
	assert.NilError(t, AutoSync(local))
	localCalls := calls()
	t.Logf("local-only edit: %d git calls\n  %s", len(localCalls), strings.Join(localCalls, "\n  "))

	assert.Assert(t, len(idle) <= 5, "idle poll spawned %d git processes", len(idle))
	assert.Assert(t, len(edit) <= 9, "edit sync spawned %d git processes", len(edit))
	for _, c := range localCalls {
		sub := gitSubcommand(strings.Fields(c))
		assert.Assert(t, !isNetworkCommand(sub), "local-only run used the network: %s", c)
	}
}
