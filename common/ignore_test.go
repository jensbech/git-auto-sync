package common

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func Test_SimpleIgnore(t *testing.T) {
	repoPath := PrepareFixture(t, "ignore").RepoPath

	ignore, err := isFileIgnoredByGit(repoPath, "1.txt")
	assert.NilError(t, err)
	assert.Equal(t, ignore, true)

	ignore, err = isFileIgnoredByGit(repoPath, "2.md")
	assert.NilError(t, err)
	assert.Equal(t, ignore, false)
}

func Test_HiddenFilesIgnore(t *testing.T) {
	repoPath := PrepareFixture(t, "ignore").RepoPath

	ignore, err := ShouldIgnoreFile(repoPath, ".hidden")
	assert.NilError(t, err)
	assert.Equal(t, ignore, false)
}

func Test_IgnoreEdgePaths(t *testing.T) {
	repoPath := PrepareFixture(t, "ignore").RepoPath

	cases := map[string]bool{
		repoPath:                                            false,
		filepath.Join(repoPath, ".git"):                     true,
		filepath.Join(repoPath, ".git", "index"):            true,
		filepath.Join(repoPath, "sub", ".git", "HEAD"):      true,
		filepath.Join(repoPath, "notes.md.swp"):             true,
		filepath.Join(repoPath, "notes.md~"):                true,
		filepath.Join(filepath.Dir(repoPath), "outside.md"): true,
		filepath.Join(repoPath, "dir", "1.txt"):             true,
		filepath.Join(repoPath, "dir", "with space.md"):     false,
	}
	for p, want := range cases {
		got, err := ShouldIgnoreFile(repoPath, p)
		assert.NilError(t, err, p)
		assert.Equal(t, got, want, p)
	}
}

func Test_IgnoreDirectoryCacheAndInvalidation(t *testing.T) {
	repoPath := PrepareFixture(t, "ignore").RepoPath
	assert.NilError(t, os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte("*.txt\nbuild/\n"), 0644))
	assert.NilError(t, os.MkdirAll(filepath.Join(repoPath, "build", "out"), 0755))
	invalidateIgnoreCache(repoPath)

	ignored, err := ShouldIgnoreFile(repoPath, filepath.Join(repoPath, "build", "out", "a.o"))
	assert.NilError(t, err)
	assert.Assert(t, ignored)

	ignoreCacheMu.Lock()
	entry, ok := repoCache(repoPath).entries["build/"]
	ignoreCacheMu.Unlock()
	assert.Assert(t, ok)
	assert.Assert(t, entry.ignored)

	ignored, err = ShouldIgnoreFile(repoPath, "debug.log")
	assert.NilError(t, err)
	assert.Assert(t, !ignored)

	assert.NilError(t, os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte("*.txt\nbuild/\n*.log\n"), 0644))
	ignored, err = ShouldIgnoreFile(repoPath, filepath.Join(repoPath, ".gitignore"))
	assert.NilError(t, err)
	assert.Assert(t, !ignored)

	ignored, err = ShouldIgnoreFile(repoPath, "debug.log")
	assert.NilError(t, err)
	assert.Assert(t, ignored)
}

func Test_IgnoreThroughSymlinkedRepoPath(t *testing.T) {
	repoPath := PrepareFixture(t, "ignore").RepoPath
	real, err := filepath.EvalSymlinks(repoPath)
	assert.NilError(t, err)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported")
	}

	ignored, err := ShouldIgnoreFile(link, filepath.Join(real, "2.md"))
	assert.NilError(t, err)
	assert.Assert(t, !ignored)

	ignored, err = ShouldIgnoreFile(link, filepath.Join(real, "1.txt"))
	assert.NilError(t, err)
	assert.Assert(t, ignored)
}

func Test_IgnoreEventThroughSymlinkedPrefix(t *testing.T) {
	repoPath := PrepareFixture(t, "ignore").RepoPath
	real, err := filepath.EvalSymlinks(repoPath)
	assert.NilError(t, err)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported")
	}

	for _, root := range []string{real, repoPath} {
		ignored, err := ShouldIgnoreFile(root, filepath.Join(link, "2.md"))
		assert.NilError(t, err)
		assert.Assert(t, !ignored, root)

		ignored, err = ShouldIgnoreFile(root, filepath.Join(link, "deleted", "gone.md"))
		assert.NilError(t, err)
		assert.Assert(t, !ignored, root)

		ignored, err = ShouldIgnoreFile(root, filepath.Join(link, "1.txt"))
		assert.NilError(t, err)
		assert.Assert(t, ignored, root)

		ignored, err = ShouldIgnoreFile(root, filepath.Join(link, ".git", "index"))
		assert.NilError(t, err)
		assert.Assert(t, ignored, root)
	}

	outside := t.TempDir()
	ignored, err := ShouldIgnoreFile(real, filepath.Join(outside, "x.md"))
	assert.NilError(t, err)
	assert.Assert(t, ignored)
}
