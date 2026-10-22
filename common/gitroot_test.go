package common

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func Test_ResolvePathMissingTailUnderSymlink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	assert.NilError(t, err)
	real := filepath.Join(base, "real")
	assert.NilError(t, os.Mkdir(real, 0755))
	link := filepath.Join(base, "link")
	assert.NilError(t, os.Symlink(real, link))

	assert.Equal(t, ResolvePath(filepath.Join(link, "gone", "repo")), filepath.Join(real, "gone", "repo"))
	assert.Equal(t, ResolvePath(link), real)
}
