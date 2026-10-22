package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"gotest.tools/v3/assert"
)

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureLog(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := log.Writer()
	log.SetOutput(c)
	t.Cleanup(func() { log.SetOutput(prev) })
	return c
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

type countingSync struct {
	mu      sync.Mutex
	calls   map[string]int
	active  map[string]int
	overlap atomic.Bool
	first   map[string]common.RepoState
	block   bool
	started chan string
}

func newCountingSync() *countingSync {
	return &countingSync{
		calls:  map[string]int{},
		active: map[string]int{},
		first:  map[string]common.RepoState{},
	}
}

func (c *countingSync) run(ctx context.Context, rc common.RepoConfig) error {
	c.mu.Lock()
	c.active[rc.RepoPath]++
	if c.active[rc.RepoPath] > 1 {
		c.overlap.Store(true)
	}
	c.calls[rc.RepoPath]++
	if c.calls[rc.RepoPath] == 1 && rc.State != nil {
		st, _ := rc.State.Get(rc.RepoPath)
		c.first[rc.RepoPath] = st
	}
	block := c.block
	started := c.started
	c.mu.Unlock()

	if started != nil {
		select {
		case started <- rc.RepoPath:
		default:
		}
	}
	if block {
		<-ctx.Done()
	}

	c.mu.Lock()
	c.active[rc.RepoPath]--
	c.mu.Unlock()
	if block {
		return ctx.Err()
	}
	return nil
}

func (c *countingSync) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[path]
}

func (c *countingSync) firstState(path string) common.RepoState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first[path]
}

func newRealDaemon(t *testing.T, s *countingSync) *Daemon {
	t.Helper()
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)
	d.watchFn = nil
	d.syncFn = s.run
	d.repoCheckInterval = 20 * time.Millisecond
	return d
}

func Test_CorruptStateFileStartsClean(t *testing.T) {
	for name, content := range map[string]string{
		"truncated": `{"version":1,"repos":[{"path":"/r/a","paus`,
		"garbage":   "\x00\x01not json at all",
		"wrongtype": `{"version":1,"repos":{"path":"/r/a"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := newTestDaemon(t, &fakeWatchers{})
			logs := captureLog(t)

			file := common.StateFilePath()
			assert.NilError(t, os.MkdirAll(filepath.Dir(file), 0o700))
			assert.NilError(t, os.WriteFile(file, []byte(content), 0o600))

			d.ensureRestored([]string{"/r/a"})
			st, ok := d.state.Get("/r/a")
			assert.Assert(t, ok)
			assert.Equal(t, st.Status, common.StatusIdle)
			assert.Assert(t, strings.Contains(logs.String(), "warning: state file"), logs.String())

			d.state.SetPaused("/r/a", true)
			d.saveState()
			data, err := os.ReadFile(file)
			assert.NilError(t, err)
			assert.Assert(t, json.Valid(data), string(data))

			r := common.NewRepoStateStore()
			n, err := r.LoadPersisted(file, nil)
			assert.NilError(t, err)
			assert.Equal(t, n, 1)
		})
	}
}

func Test_StateNotOverwrittenUntilConfigIsReadable(t *testing.T) {
	d := newTestDaemon(t, &fakeWatchers{})
	file := common.StateFilePath()
	assert.NilError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	src := common.NewRepoStateStore()
	src.Ensure("/r/a")
	src.SetPaused("/r/a", true)
	_, err := src.SavePersisted(file, nil)
	assert.NilError(t, err)
	before, err := os.ReadFile(file)
	assert.NilError(t, err)

	d.saveState()
	after, err := os.ReadFile(file)
	assert.NilError(t, err)
	assert.DeepEqual(t, before, after)

	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{"/r/a"}})
	st, _ := d.state.Get("/r/a")
	assert.Equal(t, st.Paused, true)
	h, ok := d.handle("/r/a")
	assert.Assert(t, ok)
	assert.Assert(t, h.paused.Load())
}

func Test_RestoresBranchInfoBeforeFirstSync(t *testing.T) {
	s := newCountingSync()
	d := newRealDaemon(t, s)

	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	gitRun(t, base, "init", "-q", "--bare", "-b", "main", remote)
	clone := filepath.Join(base, "clone")
	gitRun(t, base, "clone", "-q", remote, clone)
	gitRun(t, clone, "symbolic-ref", "HEAD", "refs/heads/main")
	assert.NilError(t, os.WriteFile(filepath.Join(clone, "a.md"), []byte("a"), 0o644))
	gitRun(t, clone, "add", "-A")
	gitRun(t, clone, "commit", "-q", "-m", "one")
	gitRun(t, clone, "push", "-q", "-u", "origin", "main")
	assert.NilError(t, os.WriteFile(filepath.Join(clone, "b.md"), []byte("b"), 0o644))
	gitRun(t, clone, "add", "-A")
	gitRun(t, clone, "commit", "-q", "-m", "two")

	root, err := common.RepoRoot(clone)
	assert.NilError(t, err)
	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{root}})

	waitUntil(t, func() bool { return s.count(root) >= 1 })
	first := s.firstState(root)
	assert.Equal(t, first.Branch, "main")
	assert.Equal(t, first.Upstream, "origin/main")
	assert.Equal(t, first.Ahead, 1)
	assert.Equal(t, first.Behind, 0)
}

func Test_DeletedRepoGetsMissingStateAndRecovers(t *testing.T) {
	s := newCountingSync()
	d := newRealDaemon(t, s)
	em := &eventRecorder{}
	d.emitter = em

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	assert.NilError(t, err)
	repo := filepath.Join(tmp, "notes")
	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{repo}})

	waitUntil(t, func() bool {
		st, _ := d.state.Get(repo)
		return st.ErrorCode == common.CodeRepoMissing
	})
	st, _ := d.state.Get(repo)
	assert.Equal(t, st.Status, common.StatusPreflight)
	assert.Equal(t, st.Preflight, "Repository folder not found: "+repo)
	assert.Equal(t, st.LastError, st.Preflight)
	assert.Assert(t, !strings.Contains(st.LastError, "stat "))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, s.count(repo), 0)
	assert.Equal(t, em.count(common.EventPreflight), 1)
	assert.Equal(t, common.ErrorCode(d.SyncNow(repo, false)), common.CodeRepoMissing)
	assert.Equal(t, common.ErrorCode(d.AbortRebase(repo)), common.CodeRepoMissing)
	_, err = d.readRepoSettings(repo)
	assert.Equal(t, common.ErrorCode(err), common.CodeRepoMissing)

	assert.NilError(t, os.MkdirAll(repo, 0o755))
	gitInit(t, repo)
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "note.md"), []byte("x"), 0o644))
	waitUntil(t, func() bool { return s.count(repo) >= 1 })
	st, _ = d.state.Get(repo)
	assert.Equal(t, st.ErrorCode, "")
	assert.Equal(t, st.Preflight, "")
	assert.Equal(t, em.count(common.EventRecovered), 1)

	assert.NilError(t, os.RemoveAll(repo))
	waitUntil(t, func() bool {
		st, _ := d.state.Get(repo)
		return st.ErrorCode == common.CodeRepoMissing
	})
	waitUntil(t, func() bool { return em.count(common.EventPreflight) == 2 })
	_, ok := d.handle(repo)
	assert.Assert(t, ok)
}

type eventRecorder struct {
	mu     sync.Mutex
	events []common.Event
}

func (e *eventRecorder) Emit(ev common.Event) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
}

func (e *eventRecorder) count(typ string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ev := range e.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func Test_ManyReposAtOnce(t *testing.T) {
	s := newCountingSync()
	d := newRealDaemon(t, s)

	base := t.TempDir()
	var repos []string
	for i := 0; i < 25; i++ {
		dir := filepath.Join(base, fmt.Sprintf("repo-%02d", i))
		assert.NilError(t, os.MkdirAll(dir, 0o755))
		gitInit(t, dir)
		repos = append(repos, dir)
	}
	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: repos})

	waitUntil(t, func() bool {
		for _, r := range repos {
			if s.count(r) < 1 {
				return false
			}
		}
		return true
	})
	for _, r := range repos {
		st, _ := d.state.Get(r)
		assert.Assert(t, st.Watching, r)
		assert.Assert(t, st.Branch != "", r)
	}

	d.SyncAll()
	waitUntil(t, func() bool {
		for _, r := range repos {
			if s.count(r) < 2 {
				return false
			}
		}
		return true
	})

	d.SetAllPaused(true)
	d.SyncAll()
	time.Sleep(50 * time.Millisecond)
	for _, r := range repos {
		assert.Equal(t, s.count(r), 2, r)
	}
	d.SetAllPaused(false)
	waitUntil(t, func() bool {
		for _, r := range repos {
			if s.count(r) < 3 {
				return false
			}
		}
		return true
	})

	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: repos[:10]})
	waitUntil(t, func() bool { return len(d.state.Snapshot()) == 10 })
	assert.Equal(t, len(d.watchedPaths()), 10)
	assert.Assert(t, !s.overlap.Load())
}

func Test_StopDuringSyncIsPrompt(t *testing.T) {
	s := newCountingSync()
	s.block = true
	s.started = make(chan string, 10)
	d := newRealDaemon(t, s)

	base := t.TempDir()
	var repos []string
	for i := 0; i < 3; i++ {
		dir := filepath.Join(base, fmt.Sprintf("r%d", i))
		assert.NilError(t, os.MkdirAll(dir, 0o755))
		gitInit(t, dir)
		repos = append(repos, dir)
	}
	d.ensureRestored(repos)
	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: repos})
	for range repos {
		select {
		case <-s.started:
		case <-time.After(3 * time.Second):
			t.Fatal("syncs did not start")
		}
	}

	begin := time.Now()
	assert.NilError(t, d.Stop(nil))
	assert.Assert(t, time.Since(begin) < 2*time.Second, "stop took %s", time.Since(begin))
	for _, r := range repos {
		st, _ := d.state.Get(r)
		assert.Assert(t, !st.Watching, r)
	}
	data, err := os.ReadFile(common.StateFilePath())
	assert.NilError(t, err)
	assert.Assert(t, json.Valid(data))
}

func Test_NetworkChangeRetriesOfflineRepos(t *testing.T) {
	d := newTestDaemon(t, &fakeWatchers{})
	var fp atomic.Value
	fp.Store("en0=10.0.0.2")
	d.netFingerprint = func() string { return fp.Load().(string) }
	d.netInterval = 10 * time.Millisecond

	base := t.TempDir()
	off, ok, paused := filepath.Join(base, "off"), filepath.Join(base, "ok"), filepath.Join(base, "paused")
	for _, p := range []string{off, ok, paused} {
		assert.NilError(t, os.Mkdir(p, 0o755))
	}
	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{off, ok, paused}})
	d.state.MarkOffline(off, "offline", 0)
	d.state.MarkOffline(paused, "offline", 0)
	assert.NilError(t, d.SetPaused(paused, true))
	d.state.MarkSuccess(ok, "synced")
	d.startNetworkWatcher(d.ctx)

	hOff, _ := d.handle(off)
	hOK, _ := d.handle(ok)
	hPaused, _ := d.handle(paused)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, len(hOff.immediate), 0)

	fp.Store("")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, len(hOff.immediate), 0)

	fp.Store("en0=192.168.1.4")
	waitUntil(t, func() bool { return len(hOff.immediate) == 1 })
	assert.Equal(t, len(hOK.immediate), 0)
	assert.Equal(t, len(hPaused.immediate), 0)
}
