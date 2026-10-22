package common

import (
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func Test_ServiceConfigDarwin(t *testing.T) {
	c := serviceConfig("darwin", "/opt/homebrew/bin/git-auto-sync")

	assert.Equal(t, c.Name, "git-auto-sync-daemon")
	assert.Equal(t, c.Executable, "/opt/homebrew/bin/git-auto-sync-daemon")
	assert.Equal(t, c.Option["LogDirectory"], filepath.Dir(LogPath()))
	assert.Equal(t, c.Option["RunAtLoad"], true)
	assert.Equal(t, c.Option["KeepAlive"], true)
	assert.Equal(t, c.Option["UserService"], true)

	path := strings.Split(c.EnvVars["PATH"], ":")
	assert.Assert(t, len(path) > 3, c.EnvVars["PATH"])
	assert.Equal(t, path[0], "/opt/homebrew/bin")
	assert.Equal(t, path[1], "/usr/local/bin")
	for _, want := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		found := false
		for _, p := range path {
			found = found || p == want
		}
		assert.Assert(t, found, "%s missing from %s", want, c.EnvVars["PATH"])
	}
}

func Test_ServiceConfigLinux(t *testing.T) {
	c := serviceConfig("linux", "/usr/bin/git-auto-sync")
	assert.Equal(t, c.Executable, "/usr/bin/git-auto-sync-daemon")
	assert.Equal(t, len(c.EnvVars), 0)
	_, hasLogDir := c.Option["LogDirectory"]
	assert.Assert(t, !hasLogDir)
	assert.DeepEqual(t, c.Dependencies, []string{"After=network-online.target syslog.target"})
}
