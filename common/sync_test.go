package common

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) Emit(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *eventRecorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, ev := range r.events {
		out = append(out, ev.Type)
	}
	return out
}

func (r *eventRecorder) reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

type fakeClients struct{}

func (fakeClients) HasClients() bool { return true }

type syncFixture struct {
	t      *testing.T
	env    []string
	root   string
	remote string
	a      string
	b      string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	root := t.TempDir()
	f := &syncFixture{
		t:      t,
		env:    testGitEnv(t),
		root:   root,
		remote: filepath.Join(root, "remote.git"),
		a:      filepath.Join(root, "a"),
		b:      filepath.Join(root, "b"),
	}
	f.git(root, "init", "-q", "--bare", "-b", "main", "remote.git")
	f.git(root, "clone", "-q", "remote.git", "a")
	f.write(f.a, "notes.md", "line one\n")
	f.git(f.a, "add", "-A")
	f.git(f.a, "commit", "-q", "-m", "init")
	f.git(f.a, "push", "-q", "-u", "origin", "main")
	f.git(root, "clone", "-q", "remote.git", "b")
	return f
}

func (f *syncFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := runGit(context.Background(), RepoConfig{RepoPath: dir, Env: f.env}, nil, args, nil)
	assert.NilError(f.t, err)
	return strings.TrimSpace(out.String())
}

func (f *syncFixture) write(dir, name, content string) {
	f.t.Helper()
	p := filepath.Join(dir, name)
	assert.NilError(f.t, os.MkdirAll(filepath.Dir(p), 0755))
	assert.NilError(f.t, os.WriteFile(p, []byte(content), 0644))
}

func (f *syncFixture) commitAndPush(dir, name, content string) {
	f.t.Helper()
	f.write(dir, name, content)
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "-m", "change "+name)
	f.git(dir, "push", "-q", "origin", "HEAD:refs/heads/main")
}

func (f *syncFixture) config(dir string) (RepoConfig, *eventRecorder) {
	rec := &eventRecorder{}
	return RepoConfig{
		RepoPath: dir,
		Env:      f.env,
		Emitter:  rec,
		Clients:  fakeClients{},
		State:    NewRepoStateStore(),
	}, rec
}

func (f *syncFixture) state(cfg RepoConfig) RepoState {
	st, ok := cfg.State.Get(cfg.RepoPath)
	assert.Assert(f.t, ok)
	return st
}

func (f *syncFixture) assertNotRebasing(dir string) {
	f.t.Helper()
	gitDir := f.git(dir, "rev-parse", "--absolute-git-dir")
	rebasing, err := isRebasing(gitDir)
	assert.NilError(f.t, err)
	assert.Assert(f.t, !rebasing)
}

func Test_AutoSyncCommitPushThenQuiet(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)

	f.write(f.a, "new.md", "hello\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPush, EventSynced})

	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusOK)
	assert.Assert(t, st.LastChangeTs > 0)
	assert.Assert(t, st.LastCheckTs > 0)
	assert.Equal(t, st.Ahead, 0)
	assert.Equal(t, st.Branch, "main")
	assert.Equal(t, st.Upstream, "origin/main")
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), f.git(f.a, "rev-parse", "HEAD"))

	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{})
	assert.Equal(t, f.state(cfg).Status, StatusOK)
}

func Test_AutoSyncPull(t *testing.T) {
	f := newSyncFixture(t)
	f.commitAndPush(f.b, "other.md", "from b\n")

	cfg, rec := f.config(f.a)
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventPull, EventSynced})
	_, err := os.Stat(filepath.Join(f.a, "other.md"))
	assert.NilError(t, err)
}

func Test_AutoSyncConflictGuard(t *testing.T) {
	f := newSyncFixture(t)
	f.commitAndPush(f.b, "notes.md", "line from b\n")

	cfg, rec := f.config(f.a)
	f.write(f.a, "notes.md", "line from a\n")
	err := AutoSync(cfg)
	assert.Assert(t, err != nil)
	assert.Equal(t, ClassifyError(err), CodeConflict)
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventConflict})
	f.assertNotRebasing(f.a)

	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusConflict)
	assert.DeepEqual(t, st.ConflictFiles, []string{"notes.md"})
	assert.Equal(t, st.ConflictHead, f.git(f.a, "rev-parse", "HEAD"))
	assert.Equal(t, st.ConflictUp, f.git(f.a, "rev-parse", "origin/main"))
	conflictHead := st.ConflictHead

	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{})
	assert.Equal(t, f.state(cfg).Status, StatusConflict)

	f.write(f.a, "extra.md", "local only\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit})
	st = f.state(cfg)
	assert.Equal(t, st.Status, StatusConflict)
	assert.Equal(t, st.ConflictHead, conflictHead)
	assert.Assert(t, st.ConflictHead != f.git(f.a, "rev-parse", "HEAD"))
	f.assertNotRebasing(f.a)

	rec.reset()
	forced := cfg
	forced.Force = true
	err = AutoSync(forced)
	assert.Assert(t, errors.Is(err, errRebaseFailed))
	assert.DeepEqual(t, rec.types(), []string{EventConflict})
	f.assertNotRebasing(f.a)

	rec.reset()
	oldUp := f.state(cfg).ConflictUp
	f.commitAndPush(f.b, "unrelated.md", "more from b\n")
	err = AutoSync(cfg)
	assert.Assert(t, errors.Is(err, errRebaseFailed))
	assert.DeepEqual(t, rec.types(), []string{})
	assert.Assert(t, f.state(cfg).ConflictUp != oldUp)
	f.assertNotRebasing(f.a)

	f.git(f.a, "reset", "-q", "--hard", "origin/main")
	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventRecovered})
	st = f.state(cfg)
	assert.Equal(t, st.Status, StatusOK)
	assert.Assert(t, !st.Conflict)
	assert.Equal(t, st.ConflictHead, "")
}

func Test_PushRetryRebasesOnRejection(t *testing.T) {
	f := newSyncFixture(t)
	f.write(f.a, "local.md", "local\n")
	f.git(f.a, "add", "-A")
	f.git(f.a, "commit", "-q", "-m", "local")
	f.commitAndPush(f.b, "remote.md", "remote\n")

	cfg, _ := f.config(f.a)
	ctx := context.Background()
	info, err := readRepoInfo(ctx, cfg)
	assert.NilError(t, err)
	s := &syncRun{ctx: ctx, cfg: cfg, info: info, ahead: 1}
	assert.NilError(t, s.push())
	assert.Equal(t, s.pulled, 1)
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), f.git(f.a, "rev-parse", "HEAD"))
}

func Test_PushRetryConflictAbortsRebase(t *testing.T) {
	f := newSyncFixture(t)
	f.write(f.a, "notes.md", "local edit\n")
	f.git(f.a, "add", "-A")
	f.git(f.a, "commit", "-q", "-m", "local")
	head := f.git(f.a, "rev-parse", "HEAD")
	f.commitAndPush(f.b, "notes.md", "remote edit\n")

	cfg, _ := f.config(f.a)
	ctx := context.Background()
	info, err := readRepoInfo(ctx, cfg)
	assert.NilError(t, err)
	s := &syncRun{ctx: ctx, cfg: cfg, info: info, ahead: 1}
	err = s.push()
	assert.Assert(t, errors.Is(err, errRebaseFailed))
	var conflict *rebaseConflictError
	assert.Assert(t, errors.As(err, &conflict))
	assert.DeepEqual(t, conflict.Files, []string{"notes.md"})
	assert.Equal(t, f.git(f.a, "rev-parse", "HEAD"), head)
	f.assertNotRebasing(f.a)
}

func Test_AutoSyncOfflineThenRecovered(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.git(f.a, "remote", "set-url", "origin", "http://127.0.0.1:1/unreachable.git")

	f.write(f.a, "offline.md", "queued\n")
	err := AutoSync(cfg)
	assert.Assert(t, err != nil)
	assert.Equal(t, ClassifyError(err), CodeNetwork)
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventOffline})
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusOffline)
	assert.Equal(t, st.ErrorCode, CodeNetwork)
	assert.Assert(t, strings.HasPrefix(st.LastError, "Can't reach the remote: "), st.LastError)
	assert.Assert(t, !strings.Contains(st.LastError, "exit status"), st.LastError)
	assert.Assert(t, !strings.Contains(st.LastError, "\n"), st.LastError)

	rec.reset()
	assert.Assert(t, AutoSync(cfg) != nil)
	assert.DeepEqual(t, rec.types(), []string{})

	f.git(f.a, "remote", "set-url", "origin", f.remote)
	rec.reset()
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventPush, EventRecovered, EventSynced})
	assert.Equal(t, f.state(cfg).Status, StatusOK)
}

func Test_AutoSyncPreflightReportedOnce(t *testing.T) {
	f := newSyncFixture(t)
	cfg, rec := f.config(f.a)
	f.git(f.a, "checkout", "-q", "--detach")

	assert.Assert(t, AutoSync(cfg) != nil)
	assert.DeepEqual(t, rec.types(), []string{EventPreflight})
	assert.Equal(t, rec.events[0].Code, "detached_head")
	st := f.state(cfg)
	assert.Equal(t, st.Status, StatusPreflight)
	assert.Equal(t, st.ErrorCode, "detached_head")

	rec.reset()
	assert.Assert(t, AutoSync(cfg) != nil)
	assert.DeepEqual(t, rec.types(), []string{})

	f.git(f.a, "checkout", "-q", "main")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventRecovered})
}

func Test_PreflightInLinkedWorktree(t *testing.T) {
	f := newSyncFixture(t)
	wt := filepath.Join(f.root, "wt")
	f.git(f.a, "worktree", "add", "-q", "--track", "-b", "wt", wt, "origin/main")

	cfg, rec := f.config(wt)
	assert.NilError(t, Preflight(cfg))

	f.write(wt, "from-worktree.md", "hi\n")
	assert.NilError(t, AutoSync(cfg))
	assert.DeepEqual(t, rec.types(), []string{EventCommit, EventPush, EventSynced})
	assert.Equal(t, f.git(f.remote, "rev-parse", "main"), f.git(wt, "rev-parse", "HEAD"))

	gitDir := f.git(wt, "rev-parse", "--absolute-git-dir")
	assert.NilError(t, os.Mkdir(filepath.Join(gitDir, "rebase-merge"), 0755))
	err := Preflight(cfg)
	assert.Equal(t, preflightCode(err), "rebase_in_progress")
}

func Test_PreflightNoUpstream(t *testing.T) {
	f := newSyncFixture(t)
	f.git(f.a, "checkout", "-q", "-b", "local-only")
	cfg, _ := f.config(f.a)
	err := Preflight(cfg)
	assert.Equal(t, preflightCode(err), "no_upstream")
	assert.Assert(t, strings.Contains(err.Error(), "local-only"))
}

func Test_CommitSpacesUnicodeAndTempFiles(t *testing.T) {
	f := newSyncFixture(t)
	cfg, _ := f.config(f.a)
	cfg.MessagePrefix = "host1"

	f.write(f.a, "my notes ø [1].md", "x\n")
	f.write(f.a, ".my notes.md.swp", "swap\n")
	f.write(f.a, "dir/backup.md~", "backup\n")

	committed, err := commit(context.Background(), cfg)
	assert.NilError(t, err)
	assert.Assert(t, committed)
	assert.Equal(t, f.git(f.a, "log", "-1", "--format=%B"), "host1: A my notes ø [1].md")

	tracked := f.git(f.a, "-c", "core.quotePath=false", "ls-files")
	assert.Assert(t, !strings.Contains(tracked, ".swp"))
	assert.Assert(t, !strings.Contains(tracked, "backup.md~"))
	assert.Equal(t, f.git(f.a, "diff", "--cached", "--name-only"), "")

	committed, err = commit(context.Background(), cfg)
	assert.NilError(t, err)
	assert.Assert(t, !committed)
}

func Test_CommitNoAuthor(t *testing.T) {
	f := newSyncFixture(t)
	cfg, _ := f.config(f.a)
	empty := filepath.Join(t.TempDir(), "empty-gitconfig")
	assert.NilError(t, os.WriteFile(empty, nil, 0644))
	cfg.Env = []string{"GIT_CONFIG_GLOBAL=" + empty, "GIT_CONFIG_NOSYSTEM=1"}

	f.write(f.a, "x.md", "x\n")
	_, err := commit(context.Background(), cfg)
	assert.Equal(t, ClassifyError(err), CodeNoAuthor)
}
