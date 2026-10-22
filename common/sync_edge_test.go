package common

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
}

func (f *syncFixture) hook(dir, name, body string) {
	f.t.Helper()
	gitDir := f.git(dir, "rev-parse", "--absolute-git-dir")
	p := filepath.Join(gitDir, "hooks", name)
	assert.NilError(f.t, os.MkdirAll(filepath.Dir(p), 0755))
	assert.NilError(f.t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0755))
}

func (f *syncFixture) read(dir, name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	assert.NilError(f.t, err)
	return string(data)
}

func Test_AutoSyncLocalOnly(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.git(f.a, "remote", "set-url", "origin", "http://127.0.0.1:1/unreachable.git")

	f.write(f.a, "first.md", "1\n")
	assert.Assert(t, AutoSync(cfg) != nil)
	offline := f.state(cfg)
	assert.Equal(t, offline.Status, StatusOffline)

	cfg.State.Update(cfg.RepoPath, func(st *RepoState) { st.LastCheckTs, st.LastChangeTs = 0, 0 })
	rec.reset()
	local := cfg
	local.LocalOnly = true
	f.write(f.a, "second.md", "2\n")
	assert.NilError(t, AutoSync(local))
	assert.DeepEqual(t, rec.types(), []string{EventCommit})

	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusOffline)
	assert.Equal(t, st.ErrorCode, offline.ErrorCode)
	assert.Equal(t, st.LastError, offline.LastError)
	assert.Equal(t, st.Failures, offline.Failures)
	assert.Assert(t, st.LastCheckTs > 0)
	assert.Assert(t, st.LastChangeTs > 0)
	assert.Equal(t, st.Ahead, 2)
	assert.Equal(t, f.git(f.a, "status", "--porcelain"), "")

	cfg.State.Update(cfg.RepoPath, func(st *RepoState) { st.LastCheckTs, st.LastChangeTs = 0, 0 })
	rec.reset()
	assert.NilError(t, AutoSync(local))
	assert.DeepEqual(t, rec.types(), []string{})
	st = f.state(cfg)
	assert.Assert(t, st.LastCheckTs > 0)
	assert.Equal(t, st.LastChangeTs, int64(0))
	assert.Equal(t, st.Status, StatusOffline)

	f.git(f.a, "remote", "set-url", "origin", f.remote)
	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventPush, EventRecovered, EventSynced})
}

func Test_AutoSyncLocalOnlyKeepsConflictGuard(t *testing.T) {
	f := newSyncFixture(t)
	f.commitAndPush(f.b, "notes.md", "line from b\n")
	cfg, rec := f.config(f.a)
	f.write(f.a, "notes.md", "line from a\n")
	assert.Assert(t, AutoSync(cfg) != nil)
	before := f.state(cfg)
	assert.Equal(t, before.Status, StatusConflict)
	remoteHead := f.git(f.remote, "rev-parse", "main")

	rec.reset()
	local := cfg
	local.LocalOnly = true
	local.Force = true
	f.write(f.a, "more.md", "more\n")
	assert.NilError(t, AutoSync(local))
	assert.DeepEqual(t, rec.types(), []string{EventCommit})
	after := f.state(cfg)
	assert.Equal(t, after.Status, StatusConflict)
	assert.Equal(t, after.ConflictHead, before.ConflictHead)
	assert.Equal(t, after.ConflictUp, before.ConflictUp)
	assert.DeepEqual(t, after.ConflictFiles, before.ConflictFiles)
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), remoteHead)
	f.assertNotRebasing(f.a)
}

func Test_AutoSyncLocalOnlyPreflightLeavesStateAlone(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	assert.NilError(t, AutoSync(cfg))
	f.git(f.a, "checkout", "-q", "--detach")
	f.write(f.a, "x.md", "x\n")

	local := cfg
	local.LocalOnly = true
	rec.reset()
	err := AutoSync(local)
	assert.Equal(t, preflightCode(err), "detached_head")
	assert.DeepEqual(t, rec.types(), []string{})
	assert.Equal(t, f.state(cfg).Status, StatusOK)
	assert.Assert(t, f.git(f.a, "status", "--porcelain") != "")
}

func Test_CommitMessageCapped(t *testing.T) {
	f := newSyncFixture(t)
	cfg, _ := f.config(f.a)
	for i := 0; i < 120; i++ {
		f.write(f.a, fmt.Sprintf("bulk/file-%03d.md", i), "x\n")
	}
	committed, err := commit(context.Background(), cfg)
	assert.NilError(t, err)
	assert.Assert(t, committed)

	lines := strings.Split(f.git(f.a, "log", "-1", "--format=%B"), "\n")
	assert.Equal(t, len(lines), maxMessageFiles+1)
	assert.Equal(t, lines[0], "A bulk/file-000.md")
	assert.Equal(t, lines[maxMessageFiles-1], "A bulk/file-049.md")
	assert.Equal(t, lines[maxMessageFiles], "… and 70 more")
	assert.Equal(t, f.git(f.a, "status", "--porcelain"), "")
}

func Test_CommitMessageFormat(t *testing.T) {
	changes := []stagedChange{{Status: "M", Paths: []string{"a.md"}}, {Status: "R100", Paths: []string{"old.md", "new.md"}}}
	assert.Equal(t, commitMessage(changes, ""), "M a.md\nR100 new.md")
	assert.Equal(t, commitMessage(changes, " host "), "host: M a.md\nR100 new.md")
	assert.Equal(t, commitMessage(make([]stagedChange, 0), ""), "")
}

func Test_HasCommittableEntries(t *testing.T) {
	assert.Assert(t, !hasCommittableEntries(""))
	assert.Assert(t, !hasCommittableEntries("?? .notes.md.swp\x00?? dir/backup~\x00"))
	assert.Assert(t, hasCommittableEntries("?? notes.md\x00"))
	assert.Assert(t, hasCommittableEntries(" M .notes.md.swp\x00"))
	assert.Assert(t, hasCommittableEntries("R  new.md\x00old.md\x00"))
	assert.Assert(t, hasCommittableEntries("?? .a.swp\x00 D gone.md\x00"))
}

func Test_CommitGpgSignUnavailableFailsFast(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.git(f.a, "config", "commit.gpgsign", "true")
	f.git(f.a, "config", "gpg.program", filepath.Join(f.root, "no-such-gpg"))
	f.write(f.a, "signed.md", "x\n")

	start := time.Now()
	err := AutoSync(cfg)
	assert.Assert(t, err != nil)
	assert.Assert(t, time.Since(start) < 5*time.Second)
	assert.Equal(t, ClassifyError(err), CodeConfig)
	assert.DeepEqual(t, rec.types(), []string{EventError})
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusError)
	assert.Equal(t, st.ErrorCode, CodeConfig)
	assert.Assert(t, strings.HasPrefix(st.LastError, "Commit signing failed (commit.gpgsign is on): "), st.LastError)
	assert.Assert(t, !strings.Contains(st.LastError, "\n"))
}

func Test_CommitSignerPromptingOnTTYFailsFast(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, _ := f.config(f.a)
	signer := filepath.Join(f.root, "prompting-gpg")
	assert.NilError(t, os.WriteFile(signer, []byte("#!/bin/sh\nprintf 'Passphrase: ' > /dev/tty || exit 2\nread pass < /dev/tty || exit 2\n"), 0755))
	f.git(f.a, "config", "commit.gpgsign", "true")
	f.git(f.a, "config", "gpg.program", signer)
	f.write(f.a, "signed.md", "x\n")

	start := time.Now()
	_, err := commit(context.Background(), cfg)
	assert.Assert(t, err != nil)
	assert.Assert(t, time.Since(start) < 10*time.Second)
	assert.Equal(t, ClassifyError(err), CodeConfig)
}

func Test_CommitTimeoutKillsHookProcesses(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, _ := f.config(f.a)
	f.hook(f.a, "pre-commit", "sleep 30\n")
	f.write(f.a, "slow.md", "x\n")

	orig := gitWriteTimeout
	gitWriteTimeout = 500 * time.Millisecond
	defer func() { gitWriteTimeout = orig }()

	start := time.Now()
	_, err := commit(context.Background(), cfg)
	elapsed := time.Since(start)
	assert.Equal(t, ClassifyError(err), CodeTimeout)
	assert.Assert(t, elapsed < gitKillDelay-time.Second, "took %s", elapsed)
}

func Test_LocalTimeoutIsAnErrorNotOffline(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.hook(f.a, "pre-commit", "sleep 30\n")
	f.write(f.a, "slow.md", "x\n")

	orig := gitWriteTimeout
	gitWriteTimeout = 500 * time.Millisecond
	defer func() { gitWriteTimeout = orig }()

	err := AutoSync(cfg)
	assert.Equal(t, ClassifyError(err), CodeTimeout)
	assert.DeepEqual(t, rec.types(), []string{EventError})
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusError)
	assert.Equal(t, st.ErrorCode, CodeTimeout)
	assert.Equal(t, st.LastError, "Git commit timed out after 500ms")
}

func Test_RebaseSigningFailureIsNotAConflict(t *testing.T) {
	f := newSyncFixture(t)
	f.write(f.a, "local.md", "local\n")
	f.git(f.a, "add", "-A")
	f.git(f.a, "commit", "-q", "-m", "local")
	head := f.git(f.a, "rev-parse", "HEAD")
	f.commitAndPush(f.b, "remote.md", "remote\n")
	f.git(f.a, "config", "commit.gpgsign", "true")
	f.git(f.a, "config", "gpg.program", filepath.Join(f.root, "no-such-gpg"))

	cfg, rec := f.config(f.a)
	err := AutoSync(cfg)
	assert.Assert(t, err != nil)
	assert.Equal(t, ClassifyError(err), CodeConfig)
	assert.DeepEqual(t, rec.types(), []string{EventError})
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusError)
	assert.Assert(t, !st.Conflict)
	assert.Assert(t, strings.HasPrefix(st.LastError, "Commit signing failed"), st.LastError)
	assert.Equal(t, f.git(f.a, "rev-parse", "HEAD"), head)
	f.assertNotRebasing(f.a)
}

func Test_PreCommitHookRewritesAndStages(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.hook(f.a, "pre-commit", "git diff --cached --name-only --diff-filter=AM -z | xargs -0 -I{} sh -c 'printf \"formatted\\n\" > \"{}\" && git add \"{}\"'\n")

	f.write(f.a, "messy.md", "messy   \n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPush, EventSynced})
	assert.Equal(t, f.git(f.remote, "show", "main:messy.md"), "formatted")
	assert.Equal(t, f.git(f.a, "status", "--porcelain"), "")

	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{})
}

func Test_PreCommitHookRewritesWithoutStaging(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.hook(f.a, "pre-commit", "git diff --cached --name-only --diff-filter=AM -z | xargs -0 -I{} sh -c 'printf \"formatted\\n\" > \"{}\"'\n")

	f.write(f.a, "messy.md", "messy   \n")
	assert.NilError(t, AutoSync(cfg))
	assert.Equal(t, f.git(f.remote, "show", "main:messy.md"), "messy")
	assert.NilError(t, AutoSync(cfg))
	assert.Equal(t, f.git(f.remote, "show", "main:messy.md"), "formatted")
	assert.Equal(t, f.git(f.a, "status", "--porcelain"), "")

	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{})
}

func Test_PreCommitHookFailure(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.hook(f.a, "pre-commit", "echo 'lint: trailing whitespace in messy.md' >&2\nexit 1\n")
	head := f.git(f.a, "rev-parse", "HEAD")

	f.write(f.a, "messy.md", "messy   \n")
	err := AutoSync(cfg)
	assert.Assert(t, err != nil)
	assert.Equal(t, ClassifyError(err), CodeConfig)
	assert.DeepEqual(t, rec.types(), []string{EventError})
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusError)
	assert.Equal(t, st.LastError, "Commit rejected by the pre-commit hook: lint: trailing whitespace in messy.md")
	assert.Equal(t, f.git(f.a, "rev-parse", "HEAD"), head)

	rec.reset()
	assert.Assert(t, AutoSync(cfg) != nil)
	assert.DeepEqual(t, rec.types(), []string{})
}

func Test_ConcurrentManualCommit(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	real, err := exec.LookPath("git")
	assert.NilError(t, err)
	wrapper := filepath.Join(f.root, "racing-git")
	script := "#!/bin/sh\ncase \" $* \" in\n  *\" commit -q -m \"*) '" + real + "' commit -q -m 'manual commit' ;;\nesac\nexec '" + real + "' \"$@\"\n"
	assert.NilError(t, os.WriteFile(wrapper, []byte(script), 0755))
	cfg.GitExec = wrapper

	f.write(f.a, "race.md", "x\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventPush, EventSynced})
	assert.Equal(t, f.git(f.a, "log", "-1", "--format=%s"), "manual commit")
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), f.git(f.a, "rev-parse", "HEAD"))
	assert.Equal(t, f.state(cfg).Status, StatusOK)
}

func Test_LFSNotInstalled(t *testing.T) {
	skipOnWindows(t)
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	real, err := exec.LookPath("git")
	assert.NilError(t, err)
	cfg.GitExec = real
	cfg.Env = append(append([]string{}, cfg.Env...), "PATH="+t.TempDir())

	f.git(f.a, "config", "filter.lfs.process", "git-lfs filter-process")
	f.git(f.a, "config", "filter.lfs.clean", "git-lfs clean -- %f")
	f.git(f.a, "config", "filter.lfs.smudge", "git-lfs smudge -- %f")
	f.git(f.a, "config", "filter.lfs.required", "true")
	f.write(f.a, ".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	f.write(f.a, "big.bin", "binary\n")

	err = AutoSync(cfg)
	assert.Assert(t, err != nil)
	assert.Equal(t, ClassifyError(err), CodeConfig)
	assert.DeepEqual(t, rec.types(), []string{EventError})
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusError)
	assert.Equal(t, st.LastError, lfsMissingMessage)
}

func Test_SubmoduleRepo(t *testing.T) {
	f := newSyncFixture(t)
	f.env = append(f.env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.file.allow", "GIT_CONFIG_VALUE_0=always")
	sub := filepath.Join(f.root, "sub.git")
	f.git(f.root, "init", "-q", "--bare", "-b", "main", sub)
	seed := filepath.Join(f.root, "seed")
	f.git(f.root, "clone", "-q", sub, seed)
	f.write(seed, "lib.md", "v1\n")
	f.git(seed, "add", "-A")
	f.git(seed, "commit", "-q", "-m", "v1")
	f.git(seed, "push", "-q", "origin", "HEAD:refs/heads/main")

	f.git(f.a, "submodule", "add", "-q", sub, "sub")
	f.git(f.a, "commit", "-q", "-m", "add submodule")
	f.git(f.a, "push", "-q", "origin", "HEAD:refs/heads/main")

	cfg, rec := f.config(f.a)
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{})

	f.write(filepath.Join(f.a, "sub"), "lib.md", "dirty inside the submodule\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{})

	subDir := filepath.Join(f.a, "sub")
	f.git(subDir, "commit", "-q", "-am", "v2")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPush, EventSynced})
	assert.Equal(t, rec.events[0].Message, "M sub")
	assert.Equal(t, f.state(cfg).Status, StatusOK)
}

func Test_SparseCheckoutRepo(t *testing.T) {
	f := newSyncFixture(t)
	f.write(f.a, "keep/a.md", "a\n")
	f.write(f.a, "other/b.md", "b\n")
	f.git(f.a, "add", "-A")
	f.git(f.a, "commit", "-q", "-m", "layout")
	f.git(f.a, "push", "-q", "origin", "HEAD:refs/heads/main")
	f.git(f.b, "pull", "-q", "origin", "main")
	f.git(f.a, "sparse-checkout", "set", "--cone", "keep")
	_, err := os.Stat(filepath.Join(f.a, "other", "b.md"))
	assert.Assert(t, os.IsNotExist(err))

	f.commitAndPush(f.b, "other/b.md", "b from elsewhere\n")
	cfg, rec := f.config(f.a)
	f.write(f.a, "keep/a.md", "a edited\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPull, EventPush, EventSynced})
	assert.Equal(t, f.git(f.remote, "show", "main:other/b.md"), "b from elsewhere")
	assert.Equal(t, f.git(f.remote, "show", "main:keep/a.md"), "a edited")
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), f.git(f.a, "rev-parse", "HEAD"))
}

func Test_EmptyCloneFirstCommit(t *testing.T) {
	f := newSyncFixture(t)
	empty := filepath.Join(f.root, "empty.git")
	f.git(f.root, "init", "-q", "--bare", "-b", "main", empty)
	clone := filepath.Join(f.root, "fresh")
	f.git(f.root, "clone", "-q", empty, clone)

	cfg, rec := f.config(clone)
	assert.Equal(t, preflightCode(AutoSync(cfg)), preflightNoHead)
	assert.DeepEqual(t, rec.types(), []string{EventPreflight})

	rec.reset()
	f.write(clone, "first.md", "hello\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPush, EventRecovered, EventSynced})
	assert.Equal(t, f.git(empty, "rev-parse", "main"), f.git(clone, "rev-parse", "HEAD"))
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusOK)
	assert.Equal(t, st.Upstream, "origin/main")
	assert.Equal(t, st.Ahead, 0)
}

func Test_UnbornWithoutUpstreamDoesNotCommit(t *testing.T) {
	f := newSyncFixture(t)
	repo := filepath.Join(f.root, "fresh")
	f.git(f.root, "init", "-q", "-b", "main", repo)
	f.write(repo, "first.md", "hello\n")

	cfg, rec := f.config(repo)
	err := AutoSync(cfg)
	assert.Equal(t, preflightCode(err), preflightNoHead)
	assert.Assert(t, strings.Contains(err.Error(), "no commits yet"))
	assert.DeepEqual(t, rec.types(), []string{EventPreflight})
	_, rerr := runGit(context.Background(), RepoConfig{RepoPath: repo, Env: f.env}, nil, []string{"rev-parse", "--verify", "-q", "HEAD"}, nil)
	assert.Assert(t, rerr != nil)
}

func Test_UpstreamDeletedOnRemote(t *testing.T) {
	f := newSyncFixture(t)
	f.git(f.a, "checkout", "-q", "-b", "feature")
	f.git(f.a, "push", "-q", "-u", "origin", "feature")
	cfg, rec := f.config(f.a)
	assert.NilError(t, AutoSync(cfg))

	f.git(f.remote, "branch", "-q", "-D", "feature")
	f.write(f.a, "after-delete.md", "x\n")
	rec.reset()
	err := AutoSync(cfg)
	assert.Equal(t, preflightCode(err), preflightUpstreamGone)
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPreflight})
	assert.Equal(t, rec.events[1].Code, preflightUpstreamGone)
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusPreflight)
	assert.Assert(t, strings.Contains(st.Preflight, "origin/feature"), st.Preflight)
	assert.Equal(t, f.git(f.remote, "for-each-ref", "refs/heads/feature"), "")

	rec.reset()
	assert.Assert(t, AutoSync(cfg) != nil)
	assert.DeepEqual(t, rec.types(), []string{})

	f.git(f.a, "fetch", "-q", "--prune", "origin")
	assert.Equal(t, preflightCode(AutoSync(cfg)), preflightUpstreamGone)
	assert.DeepEqual(t, rec.types(), []string{})
	assert.Equal(t, f.git(f.remote, "for-each-ref", "refs/heads/feature"), "")

	f.git(f.a, "push", "-q", "-u", "origin", "HEAD:feature")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventRecovered})
}

func Test_UpstreamForcePushedRewrite(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.commitAndPush(f.b, "secret.md", "oops\n")
	assert.NilError(t, AutoSync(cfg))
	_, err := os.Stat(filepath.Join(f.a, "secret.md"))
	assert.NilError(t, err)

	f.git(f.b, "reset", "-q", "--hard", "HEAD~1")
	f.write(f.b, "clean.md", "rewritten\n")
	f.git(f.b, "add", "-A")
	f.git(f.b, "commit", "-q", "-m", "rewritten")
	f.git(f.b, "push", "-q", "-f", "origin", "HEAD:refs/heads/main")

	rec.reset()
	f.write(f.a, "local.md", "local\n")
	assert.NilError(t, AutoSync(cfg))
	assert.Equal(t, f.state(cfg).Status, StatusOK)
	_, err = os.Stat(filepath.Join(f.a, "secret.md"))
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, f.read(f.a, "clean.md"), "rewritten\n")
	assert.Equal(t, f.read(f.a, "local.md"), "local\n")
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), f.git(f.a, "rev-parse", "HEAD"))
	assert.Equal(t, f.git(f.a, "rev-parse", "HEAD~1"), f.git(f.b, "rev-parse", "HEAD"))
}

func Test_UpstreamForcePushedRewind(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.commitAndPush(f.b, "secret.md", "oops\n")
	assert.NilError(t, AutoSync(cfg))

	f.git(f.b, "reset", "-q", "--hard", "HEAD~1")
	f.git(f.b, "push", "-q", "-f", "origin", "HEAD:refs/heads/main")
	rewound := f.git(f.b, "rev-parse", "HEAD")

	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventPull, EventSynced})
	assert.Assert(t, strings.Contains(rec.events[0].Message, "force-pushed"), rec.events[0].Message)
	assert.Equal(t, f.git(f.a, "rev-parse", "HEAD"), rewound)
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), rewound)
	_, err := os.Stat(filepath.Join(f.a, "secret.md"))
	assert.Assert(t, os.IsNotExist(err))
}

func Test_RepoFolderMissing(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(filepath.Join(f.root, "gone"))
	err := AutoSync(cfg)
	assert.Equal(t, preflightCode(err), preflightRepoMissing, "%+v", err)
	assert.DeepEqual(t, rec.types(), []string{EventPreflight})
	assert.Equal(t, f.state(cfg).ErrorCode, preflightRepoMissing)
}
