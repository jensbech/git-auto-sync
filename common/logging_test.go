package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func Test_RotatingWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "daemon.log")

	w, err := NewRotatingWriter(path, 100, 3)
	assert.NilError(t, err)

	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 20; i++ {
		_, err := w.Write([]byte(line))
		assert.NilError(t, err)
	}
	assert.NilError(t, w.Close())

	entries, err := os.ReadDir(filepath.Dir(path))
	assert.NilError(t, err)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.DeepEqual(t, names, []string{"daemon.log", "daemon.log.1", "daemon.log.2"})

	for _, n := range names {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), n))
		assert.NilError(t, err)
		assert.Assert(t, info.Size() <= 100, "%s is %d bytes", n, info.Size())
	}
}

func Test_RotatingWriterAppendsToExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	assert.NilError(t, os.WriteFile(path, []byte("old\n"), 0o644))

	w, err := NewRotatingWriter(path, 1024, 3)
	assert.NilError(t, err)
	_, err = w.Write([]byte("new\n"))
	assert.NilError(t, err)
	assert.NilError(t, w.Close())

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "old\nnew\n")
}
