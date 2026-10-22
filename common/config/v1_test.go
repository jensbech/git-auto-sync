package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kirsle/configdir"
	"gotest.tools/v3/assert"
)

func setup(t *testing.T, name string) {
	newConfigDir, err := os.MkdirTemp(os.TempDir(), "config_"+name)
	assert.NilError(t, err)
	t.Setenv("XDG_CONFIG_HOME", newConfigDir)
	t.Setenv("HOME", newConfigDir)

	configdir.Refresh()
}

func Test_SimpleWriteReadV1(t *testing.T) {
	setup(t, "SimpleWriteRead")

	c := &ConfigV1{
		Repos: []string{"/home/xyz/hello"},
		Envs:  []string{"SSH_AUTH_SOCK=/private/tmp/com.apple.launchd.74ZznY1v1F/Listeners"},
	}
	err := WriteV1(c)
	assert.NilError(t, err)

	c2, err := ReadV1()
	assert.NilError(t, err)

	assert.DeepEqual(t, c, c2)
}

func Test_ReadEmptyV1(t *testing.T) {
	setup(t, "ReadEmpty")

	c, err := ReadV1()
	assert.NilError(t, err)
	assert.Assert(t, len(c.Repos) == 0)
}

func Test_WriteV1NormalizesAndLeavesNoTempFiles(t *testing.T) {
	setup(t, "Normalize")

	c := &ConfigV1{Repos: []string{"/a/b/", "/a/b", "", "/a/./c", "/a/c"}}
	assert.NilError(t, WriteV1(c))

	c2, err := ReadV1()
	assert.NilError(t, err)
	assert.DeepEqual(t, c2.Repos, []string{"/a/b", "/a/c"})

	entries, err := os.ReadDir(configdir.LocalConfig("git-auto-sync"))
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
	assert.Equal(t, entries[0].Name(), "config.json")
}

func Test_ReadV1NormalizesHandEditedFile(t *testing.T) {
	setup(t, "HandEdited")

	dir := configdir.LocalConfig("git-auto-sync")
	assert.NilError(t, configdir.MakePath(dir))
	data := []byte(`{"repos":["/x/y/","/x/y","/x/z"],"envs":[]}`)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644))

	c, err := ReadV1()
	assert.NilError(t, err)
	assert.DeepEqual(t, c.Repos, []string{"/x/y", "/x/z"})
}

func Test_ReadV1EmptyFile(t *testing.T) {
	setup(t, "EmptyFile")

	dir := configdir.LocalConfig("git-auto-sync")
	assert.NilError(t, configdir.MakePath(dir))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "config.json"), nil, 0o644))

	c, err := ReadV1()
	assert.NilError(t, err)
	assert.Equal(t, len(c.Repos), 0)
}

func Test_ConcurrentReadsDuringWritesNeverFail(t *testing.T) {
	setup(t, "Concurrent")

	assert.NilError(t, WriteV1(&ConfigV1{Repos: []string{"/r/0"}}))

	errs := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if _, err := ReadV1(); err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		assert.NilError(t, WriteV1(&ConfigV1{Repos: []string{"/r/0", "/r/1", "/r/2"}}))
	}
	<-done
	select {
	case err := <-errs:
		t.Fatalf("read during write failed: %v", err)
	default:
	}
}
