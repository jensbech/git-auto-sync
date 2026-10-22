package common

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cp "github.com/otiai10/copy"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"

	"gotest.tools/v3/assert"
)

func PrepareFixture(t *testing.T, name string) RepoConfig {
	newRepoPath := filepath.Join(t.TempDir(), name)

	fixturePath := filepath.Join("testdata", name)
	err := cp.Copy(fixturePath, newRepoPath)
	assert.NilError(t, err)

	err = os.Rename(filepath.Join(newRepoPath, ".gitted"), filepath.Join(newRepoPath, ".git"))
	assert.NilError(t, err)

	repoConfig, err := NewRepoConfig(newRepoPath)
	assert.NilError(t, err)
	repoConfig.Env = testGitEnv(t)

	return repoConfig
}

func testGitEnv(t *testing.T) []string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "gitconfig")
	content := "[user]\n\tname = Test User\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	assert.NilError(t, os.WriteFile(cfgPath, []byte(content), 0644))
	return []string{"GIT_CONFIG_GLOBAL=" + cfgPath, "GIT_CONFIG_NOSYSTEM=1"}
}

func Test_NoChanges(t *testing.T) {
	repoConfig := PrepareFixture(t, "no_changes")

	_, err := commit(context.Background(), repoConfig)
	assert.NilError(t, err)

	r, err := git.PlainOpen(repoConfig.RepoPath)
	assert.NilError(t, err)

	head, err := r.Head()
	assert.NilError(t, err)

	assert.Equal(t, head.Hash(), plumbing.NewHash("28cc969d97ddb7640f5e1428bbc8f2947d1ffd57"))
}

func HasHeadCommit(t *testing.T, repoPath string, hash string, msg string) {
	r, err := git.PlainOpen(repoPath)
	assert.NilError(t, err)

	head, err := r.Head()
	assert.NilError(t, err)

	assert.Assert(t, head.Hash() != plumbing.NewHash(hash))

	commit, err := r.CommitObject(head.Hash())
	assert.NilError(t, err)

	parent, err := commit.Parent(0)
	assert.NilError(t, err)
	assert.Equal(t, parent.ID(), plumbing.NewHash(hash))
	assert.Equal(t, commit.Message, msg)
}

func Test_NewFile(t *testing.T) {
	repoConfig := PrepareFixture(t, "new_file")

	_, err := commit(context.Background(), repoConfig)
	assert.NilError(t, err)

	HasHeadCommit(t, repoConfig.RepoPath, "28cc969d97ddb7640f5e1428bbc8f2947d1ffd57", "A 2.md\n")
}

func Test_OneFileChange(t *testing.T) {
	repoConfig := PrepareFixture(t, "one_file_change")

	_, err := commit(context.Background(), repoConfig)
	assert.NilError(t, err)

	HasHeadCommit(t, repoConfig.RepoPath, "28cc969d97ddb7640f5e1428bbc8f2947d1ffd57", "M 1.md\n")
}

func Test_VimSwapFile(t *testing.T) {
	repoConfig := PrepareFixture(t, "vim_swap_file")

	_, err := commit(context.Background(), repoConfig)
	assert.NilError(t, err)

	r, err := git.PlainOpen(repoConfig.RepoPath)
	assert.NilError(t, err)

	head, err := r.Head()
	assert.NilError(t, err)

	assert.Equal(t, head.Hash(), plumbing.NewHash("28cc969d97ddb7640f5e1428bbc8f2947d1ffd57"))
}

func Test_MultipleFileChange(t *testing.T) {
	repoConfig := PrepareFixture(t, "multiple_file_change")

	_, err := commit(context.Background(), repoConfig)
	assert.NilError(t, err)

	HasHeadCommit(t, repoConfig.RepoPath, "7058b6b292ee3d1382670334b5f29570a1117ef1", `M 1.md
D dirA/2.md
A dirB/3.md
`)
}
