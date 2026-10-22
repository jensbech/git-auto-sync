package common

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func Test_ParseRepoSettings(t *testing.T) {
	exe, err := os.Executable()
	assert.NilError(t, err)

	tests := []struct {
		name     string
		raw      map[string]string
		poll     time.Duration
		batch    time.Duration
		exec     string
		prefix   string
		warnings int
	}{
		{"defaults", map[string]string{}, DefaultPollInterval, 0, "", "", 0},
		{"valid", map[string]string{"syncinterval": "120", "batchwindow": "300"}, 120 * time.Second, 300 * time.Second, "", "", 0},
		{"zero poll clamps", map[string]string{"syncinterval": "0"}, MinPollInterval, 0, "", "", 1},
		{"negative poll clamps", map[string]string{"syncinterval": "-5"}, MinPollInterval, 0, "", "", 1},
		{"huge poll clamps", map[string]string{"syncinterval": "999999"}, MaxPollInterval, 0, "", "", 1},
		{"non-numeric poll uses default", map[string]string{"syncinterval": "10m"}, DefaultPollInterval, 0, "", "", 1},
		{"negative batch", map[string]string{"batchwindow": "-1"}, DefaultPollInterval, 0, "", "", 1},
		{"huge batch clamps", map[string]string{"batchwindow": "100000"}, DefaultPollInterval, MaxBatchWindow, "", "", 1},
		{"non-numeric batch", map[string]string{"batchwindow": "soon"}, DefaultPollInterval, 0, "", "", 1},
		{"missing exec ignored", map[string]string{"exec": "/does/not/exist/git"}, DefaultPollInterval, 0, "", "", 1},
		{"valid exec", map[string]string{"exec": exe}, DefaultPollInterval, 0, exe, "", 0},
		{"prefix", map[string]string{"messageprefix": " laptop "}, DefaultPollInterval, 0, "", "laptop", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, warnings := ParseRepoSettings(tc.raw)
			assert.Equal(t, s.PollInterval, tc.poll)
			assert.Equal(t, s.BatchWindow, tc.batch)
			assert.Equal(t, s.GitExec, tc.exec)
			assert.Equal(t, s.MessagePrefix, tc.prefix)
			assert.Equal(t, len(warnings), tc.warnings, "%v", warnings)
		})
	}
}

func Test_ValidateSetting(t *testing.T) {
	tests := []struct {
		setting string
		value   string
		want    string
		ok      bool
	}{
		{SettingPollInterval, "30", "30", true},
		{SettingPollInterval, " 600 ", "600", true},
		{SettingPollInterval, "86400", "86400", true},
		{SettingPollInterval, "", "", true},
		{SettingPollInterval, "0", "", false},
		{SettingPollInterval, "29", "", false},
		{SettingPollInterval, "86401", "", false},
		{SettingPollInterval, "abc", "", false},
		{SettingBatchWindow, "0", "0", true},
		{SettingBatchWindow, "86400", "86400", true},
		{SettingBatchWindow, "-1", "", false},
		{SettingBatchWindow, "86401", "", false},
		{SettingExec, "/does/not/exist", "", false},
		{SettingMessagePrefix, "host-a", "host-a", true},
		{SettingMessagePrefix, "a\nb", "", false},
		{"bogus", "1", "", false},
	}
	for _, tc := range tests {
		got, err := ValidateSetting(tc.setting, tc.value)
		if tc.ok {
			assert.NilError(t, err, "%s=%q", tc.setting, tc.value)
			assert.Equal(t, got, tc.want)
		} else {
			assert.Assert(t, err != nil, "%s=%q should fail", tc.setting, tc.value)
			assert.Equal(t, ErrorCode(err), CodeConfig)
		}
	}
}

func Test_NewRepoConfigReadsGitConfig(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	run("init", "-q")
	run("config", "auto-sync.syncInterval", "0")
	run("config", "auto-sync.batchWindow", "45")
	run("config", "auto-sync.messagePrefix", "box")

	cfg, err := NewRepoConfig(repo)
	assert.NilError(t, err)
	assert.Equal(t, cfg.PollInterval, MinPollInterval)
	assert.Equal(t, cfg.BatchWindow, 45*time.Second)
	assert.Equal(t, cfg.MessagePrefix, "box")

	_, err = NewRepoConfig(filepath.Join(repo, "missing"))
	assert.Assert(t, err != nil)
}
