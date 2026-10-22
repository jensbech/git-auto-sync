package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"github.com/kirsle/configdir"
	"gotest.tools/v3/assert"
)

type fakeWatchers struct {
	running    int32
	maxRunning int32
	starts     int32
	exitDelay  time.Duration
}

func (f *fakeWatchers) watch(ctx context.Context, repoPath string, h *watcherHandle, envs []string, delay time.Duration) {
	n := atomic.AddInt32(&f.running, 1)
	for {
		m := atomic.LoadInt32(&f.maxRunning)
		if n <= m || atomic.CompareAndSwapInt32(&f.maxRunning, m, n) {
			break
		}
	}
	atomic.AddInt32(&f.starts, 1)
	<-ctx.Done()
	time.Sleep(f.exitDelay)
	atomic.AddInt32(&f.running, -1)
}

func newTestDaemon(t *testing.T, f *fakeWatchers) *Daemon {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	configdir.Refresh()

	d := &Daemon{}
	d.init()
	d.watchFn = f.watch
	t.Cleanup(func() {
		d.cancel()
		d.wg.Wait()
	})
	return d
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func Test_RestartWatcherNeverRunsTwo(t *testing.T) {
	f := &fakeWatchers{exitDelay: 30 * time.Millisecond}
	d := newTestDaemon(t, f)

	d.mu.Lock()
	d.startWatcherLocked(d.ctx, "/repo", 0)
	d.mu.Unlock()
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.starts) == 1 })

	for i := 0; i < 5; i++ {
		d.restartWatcher("/repo")
	}
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.starts) >= 2 })
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, atomic.LoadInt32(&f.maxRunning), int32(1))
	assert.Equal(t, atomic.LoadInt32(&f.running), int32(1))
	_, ok := d.handle("/repo")
	assert.Assert(t, ok)
}

func Test_RestartWatcherKeepsPaused(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)

	d.mu.Lock()
	d.startWatcherLocked(d.ctx, "/repo", 0)
	d.mu.Unlock()

	assert.NilError(t, d.SetPaused("/repo", true))
	d.restartWatcher("/repo")

	h, ok := d.handle("/repo")
	assert.Assert(t, ok)
	assert.Assert(t, h.paused.Load())
	st, _ := d.state.Get("/repo")
	assert.Equal(t, st.Status, common.StatusPaused)
}

func Test_ReconcileRemovesStateAfterStop(t *testing.T) {
	f := &fakeWatchers{exitDelay: 20 * time.Millisecond}
	d := newTestDaemon(t, f)

	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{"/a", "/b"}})
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.starts) == 2 })

	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{"/b"}})
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.running) == 1 })
	time.Sleep(20 * time.Millisecond)

	_, ok := d.state.Get("/a")
	assert.Assert(t, !ok)
	_, ok = d.handle("/a")
	assert.Assert(t, !ok)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
}

func Test_AddRepoResolvesRootAndValidates(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)

	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "notes", "deep")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	root, err := d.AddRepo(sub)
	assert.NilError(t, err)
	want, _ := filepath.EvalSymlinks(repo)
	assert.Equal(t, root, want)

	_, err = d.AddRepo(repo)
	assert.Equal(t, common.ErrorCode(err), common.CodeAlreadyExists)

	_, err = d.AddRepo(t.TempDir())
	assert.Equal(t, common.ErrorCode(err), common.CodeNotARepo)

	config, err := cfg.Read()
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Repos, []string{want})
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.starts) == 1 })

	assert.NilError(t, d.RemoveRepo(sub))
	config, err = cfg.Read()
	assert.NilError(t, err)
	assert.Equal(t, len(config.Repos), 0)

	err = d.RemoveRepo(sub)
	assert.Equal(t, common.ErrorCode(err), common.CodeNotFound)
}

func Test_AddRepoMatchesSymlinkedConfigEntry(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)

	base := t.TempDir()
	repo := filepath.Join(base, "real", "repo")
	assert.NilError(t, os.MkdirAll(repo, 0o755))
	gitInit(t, repo)
	assert.NilError(t, os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")))
	viaLink := filepath.Join(base, "link", "repo")
	resolved, err := filepath.EvalSymlinks(repo)
	assert.NilError(t, err)

	assert.NilError(t, cfg.Write(&cfg.Config{Repos: []string{viaLink}}))
	d.reconcileWatchers(d.ctx, &cfg.Config{Repos: []string{viaLink, resolved, repo}})
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.starts) == 1 })
	assert.DeepEqual(t, d.watchedPaths(), []string{viaLink})

	got, err := d.AddRepo(resolved)
	assert.Equal(t, common.ErrorCode(err), common.CodeAlreadyExists)
	assert.Equal(t, got, viaLink)
	got, err = d.AddRepo(filepath.Join(repo, "."))
	assert.Equal(t, common.ErrorCode(err), common.CodeAlreadyExists)
	assert.Equal(t, got, viaLink)

	config, err := cfg.Read()
	assert.NilError(t, err)
	assert.DeepEqual(t, config.Repos, []string{viaLink})
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, len(d.watchedPaths()), 1)

	key, _, ok := d.lookupWatcher(resolved)
	assert.Assert(t, ok)
	assert.Equal(t, key, viaLink)

	assert.NilError(t, d.RemoveRepo(resolved))
	config, err = cfg.Read()
	assert.NilError(t, err)
	assert.Equal(t, len(config.Repos), 0)
}

func Test_RemoveRepoWorksForDeletedPath(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)

	gone := filepath.Join(t.TempDir(), "gone")
	assert.NilError(t, cfg.Write(&cfg.Config{Repos: []string{gone}}))
	assert.NilError(t, d.RemoveRepo(gone+"/"))
}

func Test_WriteRepoSettingsValidates(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)

	repo := t.TempDir()
	gitInit(t, repo)
	root, err := d.AddRepo(repo)
	assert.NilError(t, err)

	err = d.writeRepoSettings(root, map[string]string{"pollInterval": "0"})
	assert.Equal(t, common.ErrorCode(err), common.CodeConfig)

	err = d.writeRepoSettings(root, map[string]string{"nope": "1"})
	assert.Equal(t, common.ErrorCode(err), common.CodeConfig)

	assert.NilError(t, d.writeRepoSettings(root, map[string]string{"pollInterval": "120", "batchWindow": "30"}))
	got, err := d.readRepoSettings(root)
	assert.NilError(t, err)
	assert.Equal(t, got["pollInterval"], 120)
	assert.Equal(t, got["batchWindow"], 30)

	assert.NilError(t, d.writeRepoSettings(root, map[string]string{"batchWindow": ""}))
	got, err = d.readRepoSettings(root)
	assert.NilError(t, err)
	assert.Equal(t, got["batchWindow"], nil)

	err = d.writeRepoSettings(filepath.Join(t.TempDir(), "x"), map[string]string{"pollInterval": "60"})
	assert.Equal(t, common.ErrorCode(err), common.CodeNotWatching)
}

func Test_SyncNowAndAbortRebaseErrors(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)

	err := d.SyncNow("/nowhere", false)
	assert.Equal(t, common.ErrorCode(err), common.CodeNotWatching)

	repo := t.TempDir()
	gitInit(t, repo)
	root, err := d.AddRepo(repo)
	assert.NilError(t, err)

	assert.NilError(t, d.SyncNow(root, true))
	h, _ := d.handle(root)
	assert.Assert(t, h.force.Load())
	assert.Equal(t, len(h.immediate), 1)

	err = d.AbortRebase(root)
	assert.Equal(t, common.ErrorCode(err), common.CodeBadRequest)

	assert.NilError(t, d.SetPaused(root, true))
	err = d.SyncNow(root, false)
	assert.Equal(t, common.ErrorCode(err), common.CodeBadRequest)
}
