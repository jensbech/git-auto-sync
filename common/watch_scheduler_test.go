package common

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

type fakeSync struct {
	mu      sync.Mutex
	calls   []RepoConfig
	active  int32
	overlap int32
	fn      func(context.Context, RepoConfig) error
}

func (f *fakeSync) run(ctx context.Context, cfg RepoConfig) error {
	if atomic.AddInt32(&f.active, 1) > 1 {
		atomic.StoreInt32(&f.overlap, 1)
	}
	defer atomic.AddInt32(&f.active, -1)
	f.mu.Lock()
	f.calls = append(f.calls, cfg)
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx, cfg)
	}
	return nil
}

func (f *fakeSync) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSync) last() RepoConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func testSchedulerConfig(t *testing.T) RepoConfig {
	t.Helper()
	repo := t.TempDir()
	state := NewRepoStateStore()
	state.Ensure(repo)
	return RepoConfig{
		RepoPath:     repo,
		PollInterval: time.Hour,
		FSLag:        20 * time.Millisecond,
		State:        state,
		Trigger:      make(chan bool, 100),
		Immediate:    make(chan struct{}, 1),
		ForcePending: &atomic.Bool{},
		Paused:       &atomic.Bool{},
		SyncMu:       &sync.Mutex{},
	}
}

func waitFor(t *testing.T, cond func() bool) {
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

func startScheduler(t *testing.T, cfg RepoConfig, f *fakeSync, tweak func(*syncScheduler)) (context.CancelFunc, chan struct{}) {
	t.Helper()
	s := newSyncScheduler(cfg, f.run)
	s.cooldown = 0
	if tweak != nil {
		tweak(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

func Test_SchedulerDebouncesFileEvents(t *testing.T) {
	cfg := testSchedulerConfig(t)
	f := &fakeSync{}
	startScheduler(t, cfg, f, nil)

	waitFor(t, func() bool { return f.count() == 1 })
	for i := 0; i < 10; i++ {
		cfg.Trigger <- true
	}
	waitFor(t, func() bool { return f.count() == 2 })
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, f.count(), 2)
	assert.Equal(t, atomic.LoadInt32(&f.overlap), int32(0))
}

func Test_SchedulerDefersEventsDuringCooldown(t *testing.T) {
	cfg := testSchedulerConfig(t)
	f := &fakeSync{}
	startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.cooldown = 150 * time.Millisecond
	})

	waitFor(t, func() bool { return f.count() == 1 })
	start := time.Now()
	cfg.Trigger <- true
	cfg.Trigger <- true
	waitFor(t, func() bool { return f.count() == 2 })
	assert.Assert(t, time.Since(start) >= 100*time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	assert.Equal(t, f.count(), 2)
}

func Test_SchedulerOfflineBackoffPublishesNextRetry(t *testing.T) {
	cfg := testSchedulerConfig(t)
	f := &fakeSync{fn: func(_ context.Context, c RepoConfig) error {
		if c.LocalOnly {
			return nil
		}
		c.State.MarkOffline(c.RepoPath, "network down", 0)
		return errors.New("network down")
	}}
	startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.offlineBackoffMin = time.Hour
		s.offlineBackoff = time.Hour
	})

	waitFor(t, func() bool { return f.count() == 1 })
	waitFor(t, func() bool {
		st, _ := cfg.State.Get(cfg.RepoPath)
		return st.NextRetryTs > 0
	})
	st, _ := cfg.State.Get(cfg.RepoPath)
	retryTs := st.NextRetryTs
	assert.Assert(t, retryTs >= time.Now().Add(59*time.Minute).Unix())

	for i := 0; i < 5; i++ {
		cfg.Trigger <- true
	}
	waitFor(t, func() bool { return f.count() == 2 })
	assert.Equal(t, f.last().LocalOnly, true)
	assert.Equal(t, f.last().Force, false)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, f.count(), 2)
	st, _ = cfg.State.Get(cfg.RepoPath)
	assert.Equal(t, st.NextRetryTs, retryTs)
	assert.Equal(t, st.Status, StatusOffline)

	cfg.ForcePending.Store(true)
	cfg.Immediate <- struct{}{}
	waitFor(t, func() bool { return f.count() == 3 })
	assert.Equal(t, f.last().Force, true)
	assert.Equal(t, f.last().LocalOnly, false)
}

func Test_SchedulerOfflineLocalRunsKeepNetworkSchedule(t *testing.T) {
	cfg := testSchedulerConfig(t)
	var online atomic.Bool
	f := &fakeSync{fn: func(_ context.Context, c RepoConfig) error {
		if c.LocalOnly {
			return nil
		}
		if online.Load() {
			c.State.MarkSuccess(c.RepoPath, "synced")
			return nil
		}
		c.State.MarkOffline(c.RepoPath, "network down", 0)
		return errors.New("network down")
	}}
	startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.offlineBackoffMin = 300 * time.Millisecond
		s.offlineBackoff = 300 * time.Millisecond
	})

	waitFor(t, func() bool { return f.count() == 1 })
	start := time.Now()
	cfg.Trigger <- true
	waitFor(t, func() bool { return f.count() == 2 })
	assert.Equal(t, f.last().LocalOnly, true)
	assert.Assert(t, time.Since(start) < 250*time.Millisecond)

	online.Store(true)
	waitFor(t, func() bool { return f.count() == 3 })
	assert.Equal(t, f.last().LocalOnly, false)
	assert.Assert(t, time.Since(start) >= 200*time.Millisecond)

	cfg.Trigger <- true
	waitFor(t, func() bool { return f.count() == 4 })
	assert.Equal(t, f.last().LocalOnly, false)
}

func Test_SchedulerErrorBackoffCommitsLocallyThenRetries(t *testing.T) {
	cfg := testSchedulerConfig(t)
	var fail atomic.Bool
	fail.Store(true)
	f := &fakeSync{fn: func(_ context.Context, c RepoConfig) error {
		if c.LocalOnly {
			return nil
		}
		if fail.Load() {
			c.State.MarkErrorCode(c.RepoPath, CodePushRejected, "push rejected")
			return errors.New("push rejected")
		}
		return nil
	}}
	startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.errorBackoff = 250 * time.Millisecond
	})

	waitFor(t, func() bool { return f.count() == 1 })
	fail.Store(false)
	cfg.Trigger <- true
	waitFor(t, func() bool { return f.count() == 2 })
	assert.Equal(t, f.last().LocalOnly, true)
	waitFor(t, func() bool { return f.count() == 3 })
	assert.Equal(t, f.last().LocalOnly, false)
}

func Test_SchedulerPreflightDefersFileEvents(t *testing.T) {
	cfg := testSchedulerConfig(t)
	f := &fakeSync{fn: func(_ context.Context, c RepoConfig) error {
		c.State.MarkPreflight(c.RepoPath, "rebase in progress")
		return errors.New("rebase in progress")
	}}
	startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.errorBackoff = time.Hour
	})

	waitFor(t, func() bool { return f.count() == 1 })
	cfg.Trigger <- true
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, f.count(), 1)
}

func Test_SchedulerShutdownDuringSync(t *testing.T) {
	cfg := testSchedulerConfig(t)
	started := make(chan struct{})
	var sawCancel atomic.Bool
	f := &fakeSync{fn: func(ctx context.Context, c RepoConfig) error {
		close(started)
		<-ctx.Done()
		sawCancel.Store(true)
		return ctx.Err()
	}}
	cancel, done := startScheduler(t, cfg, f, nil)

	<-started
	cfg.Trigger <- true
	cfg.Immediate <- struct{}{}
	begin := time.Now()
	cancel()
	<-done
	assert.Assert(t, time.Since(begin) < 500*time.Millisecond)
	assert.Assert(t, sawCancel.Load())
	assert.Equal(t, f.count(), 1)
	st, _ := cfg.State.Get(cfg.RepoPath)
	assert.Equal(t, st.Failures, 0)
	assert.Equal(t, st.NextRetryTs, int64(0))
}

type recordEmitter struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordEmitter) Emit(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recordEmitter) byType(typ string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, ev := range r.events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func Test_SchedulerStopsWhenRepoFolderDisappears(t *testing.T) {
	cfg := testSchedulerConfig(t)
	em := &recordEmitter{}
	cfg.Emitter = em
	f := &fakeSync{}
	s := newSyncScheduler(cfg, f.run)
	s.cooldown = 0
	s.existInterval = time.Hour
	result := make(chan bool, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { result <- s.run(ctx) }()

	waitFor(t, func() bool { return f.count() == 1 })
	assert.NilError(t, os.Remove(cfg.RepoPath))
	cfg.Trigger <- true

	select {
	case missing := <-result:
		assert.Assert(t, missing)
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not stop for a missing repo")
	}
	assert.Equal(t, f.count(), 1)

	st, _ := cfg.State.Get(cfg.RepoPath)
	assert.Equal(t, st.Status, StatusPreflight)
	assert.Equal(t, st.ErrorCode, CodeRepoMissing)
	assert.Equal(t, st.Preflight, "Repository folder not found: "+cfg.RepoPath)
	assert.Equal(t, st.LastError, st.Preflight)

	evs := em.byType(EventPreflight)
	assert.Equal(t, len(evs), 1)
	assert.Equal(t, evs[0].Code, CodeRepoMissing)
}

func Test_WatchRecoversWhenRepoFolderReturns(t *testing.T) {
	prev := repoCheckEvery
	repoCheckEvery = 20 * time.Millisecond
	t.Cleanup(func() { repoCheckEvery = prev })

	cfg := testSchedulerConfig(t)
	em := &recordEmitter{}
	cfg.Emitter = em
	f := &fakeSync{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WatchForChangesWith(ctx, cfg, f.run)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitFor(t, func() bool { return f.count() == 1 })
	assert.NilError(t, os.Remove(cfg.RepoPath))
	waitFor(t, func() bool {
		st, _ := cfg.State.Get(cfg.RepoPath)
		return st.ErrorCode == CodeRepoMissing
	})
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, len(em.byType(EventPreflight)), 1)
	assert.Equal(t, f.count(), 1)

	assert.NilError(t, os.Mkdir(cfg.RepoPath, 0o755))
	waitFor(t, func() bool { return f.count() == 2 })
	st, _ := cfg.State.Get(cfg.RepoPath)
	assert.Equal(t, st.ErrorCode, "")
	assert.Equal(t, st.Preflight, "")
	assert.Equal(t, st.Watching, true)
	assert.Equal(t, len(em.byType(EventRecovered)), 1)
}

func Test_RepoMissingRespectsPaused(t *testing.T) {
	s := NewRepoStateStore()
	s.Ensure("/r")
	s.SetPaused("/r", true)
	assert.Assert(t, s.MarkRepoMissing("/r", RepoMissingMessage("/r")))
	assert.Assert(t, !s.MarkRepoMissing("/r", RepoMissingMessage("/r")))
	assert.Assert(t, s.ClearRepoMissing("/r"))
	st, _ := s.Get("/r")
	assert.Equal(t, st.Status, StatusPaused)
	assert.Equal(t, st.Preflight, "")
	assert.Assert(t, !s.ClearRepoMissing("/r"))
}

func Test_PreflightCodeSetAndCleared(t *testing.T) {
	s := NewRepoStateStore()
	s.MarkPreflightCode("/r", "rebase_in_progress", "rebase in progress")
	st, _ := s.Get("/r")
	assert.Equal(t, st.Status, StatusPreflight)
	assert.Equal(t, st.ErrorCode, "rebase_in_progress")
	s.ClearPreflight("/r")
	st, _ = s.Get("/r")
	assert.Equal(t, st.Status, StatusIdle)
	assert.Equal(t, st.ErrorCode, "")
	assert.Equal(t, st.Preflight, "")
}

func Test_ErrorStatesClearStalePreflight(t *testing.T) {
	s := NewRepoStateStore()
	s.MarkPreflight("/r", "no upstream")
	s.MarkErrorCode("/r", CodeAuth, "auth failed")
	st, _ := s.Get("/r")
	assert.Equal(t, st.Preflight, "")

	s.MarkPreflight("/r", "no upstream")
	s.MarkOffline("/r", "offline", 0)
	st, _ = s.Get("/r")
	assert.Equal(t, st.Preflight, "")

	s.MarkPreflight("/r", "no upstream")
	s.MarkConflict("/r", []string{"a"}, "rebase conflict")
	st, _ = s.Get("/r")
	assert.Equal(t, st.Preflight, "")
}

func Test_WatchFollowsSymlinkedRepoPath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	assert.NilError(t, err)
	target := filepath.Join(base, "real")
	assert.NilError(t, os.Mkdir(target, 0o755))
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = target
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	link := filepath.Join(base, "link")
	assert.NilError(t, os.Symlink(target, link))

	cfg := testSchedulerConfig(t)
	cfg.RepoPath = link
	cfg.State.Ensure(link)
	cfg.InitialDelay = time.Hour
	f := &fakeSync{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WatchForChangesWith(ctx, cfg, f.run)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	time.Sleep(300 * time.Millisecond)
	assert.NilError(t, os.WriteFile(filepath.Join(link, "note.md"), []byte("x"), 0o644))
	waitFor(t, func() bool { return f.count() >= 1 })
	assert.Equal(t, f.last().RepoPath, link)
}

func Test_SchedulerOfflineRetriesWithGrowingDelay(t *testing.T) {
	cfg := testSchedulerConfig(t)
	var times []time.Time
	var mu sync.Mutex
	f := &fakeSync{fn: func(_ context.Context, c RepoConfig) error {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		c.State.MarkOffline(c.RepoPath, "offline", 0)
		return errors.New("offline")
	}}
	startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.offlineBackoffMin = 40 * time.Millisecond
		s.offlineBackoff = 40 * time.Millisecond
		s.offlineBackoffMax = 160 * time.Millisecond
	})

	waitFor(t, func() bool { return f.count() >= 4 })
	mu.Lock()
	defer mu.Unlock()
	gap1 := times[2].Sub(times[1])
	gap0 := times[1].Sub(times[0])
	assert.Assert(t, gap1 > gap0, "gaps %s then %s", gap0, gap1)
}

func Test_SchedulerStopsPromptlyDuringBackoff(t *testing.T) {
	cfg := testSchedulerConfig(t)
	f := &fakeSync{fn: func(_ context.Context, c RepoConfig) error {
		return errors.New("boom")
	}}
	cancel, done := startScheduler(t, cfg, f, func(s *syncScheduler) {
		s.errorBackoff = time.Hour
	})
	waitFor(t, func() bool { return f.count() == 1 })
	cfg.Trigger <- true

	start := time.Now()
	cancel()
	<-done
	assert.Assert(t, time.Since(start) < 500*time.Millisecond)
	assert.Equal(t, f.count(), 1)
}

func Test_SchedulerSkipsWhilePaused(t *testing.T) {
	cfg := testSchedulerConfig(t)
	cfg.Paused.Store(true)
	f := &fakeSync{}
	startScheduler(t, cfg, f, nil)

	cfg.Immediate <- struct{}{}
	cfg.Trigger <- true
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, f.count(), 0)

	cfg.Paused.Store(false)
	cfg.Immediate <- struct{}{}
	waitFor(t, func() bool { return f.count() == 1 })
}

func Test_SchedulerSharesSyncMutex(t *testing.T) {
	cfg := testSchedulerConfig(t)
	f := &fakeSync{fn: func(context.Context, RepoConfig) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}}
	startScheduler(t, cfg, f, nil)
	startScheduler(t, cfg, f, nil)

	for i := 0; i < 5; i++ {
		cfg.Immediate <- struct{}{}
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, func() bool { return f.count() >= 4 })
	assert.Equal(t, atomic.LoadInt32(&f.overlap), int32(0))
}
