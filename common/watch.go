package common

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rjeczalik/notify"
	"github.com/ztrue/tracerr"
)

type ClientChecker interface {
	HasClients() bool
}

type RepoConfig struct {
	RepoPath      string
	PollInterval  time.Duration
	FSLag         time.Duration
	BatchWindow   time.Duration
	GitExec       string
	Env           []string
	Emitter       EventEmitter
	Clients       ClientChecker
	State         *RepoStateStore
	Trigger       chan bool
	Paused        *atomic.Bool
	Force         bool
	LocalOnly     bool
	MessagePrefix string
	Immediate     chan struct{}
	ForcePending  *atomic.Bool
	SyncMu        *sync.Mutex
	InitialDelay  time.Duration
}

var repoCheckEvery = RepoCheckInterval

type AwakeNotifier interface {
	Start(chan bool) error
}

const (
	syncCooldown      = 5 * time.Second
	errorBackoffMin   = 1 * time.Second
	errorBackoffMax   = 60 * time.Second
	offlineBackoffMin = 30 * time.Second
	offlineBackoffMax = 10 * time.Minute
)

func NewRepoConfig(repoPath string) (RepoConfig, error) {
	info, err := os.Stat(repoPath)
	if err != nil {
		return RepoConfig{}, tracerr.Wrap(err)
	}
	if !info.IsDir() {
		return RepoConfig{}, tracerr.Wrap(fmt.Errorf("%s is not a directory", repoPath))
	}

	raw, err := ReadAutoSyncGitConfig(repoPath)
	if err != nil {
		log.Printf("config: repo=%s cannot read auto-sync settings, using defaults: %v", repoPath, err)
		raw = map[string]string{}
	}

	settings, warnings := ParseRepoSettings(raw)
	for _, w := range warnings {
		log.Printf("config: repo=%s %s", repoPath, w)
	}

	return RepoConfig{
		RepoPath:      repoPath,
		PollInterval:  settings.PollInterval,
		FSLag:         1 * time.Second,
		BatchWindow:   settings.BatchWindow,
		GitExec:       settings.GitExec,
		MessagePrefix: settings.MessagePrefix,
	}, nil
}

func (cfg RepoConfig) isPaused() bool {
	if cfg.Paused == nil {
		return false
	}
	return cfg.Paused.Load()
}

func WatchForChanges(ctx context.Context, cfg RepoConfig) error {
	return WatchForChangesWith(ctx, cfg, AutoSyncCtx)
}

func WatchForChangesWith(ctx context.Context, cfg RepoConfig, syncFn func(context.Context, RepoConfig) error) error {
	repoPath := cfg.RepoPath
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	log.Printf("watch: starting for repo=%s poll=%s batch=%s", repoPath, cfg.PollInterval, cfg.BatchWindow)

	if cfg.State != nil {
		cfg.State.SetWatching(repoPath, true)
		cfg.State.SetSettings(repoPath, int(cfg.PollInterval/time.Second), int(cfg.BatchWindow/time.Second))
		defer cfg.State.SetWatching(repoPath, false)
	}

	if cfg.Trigger == nil {
		cfg.Trigger = make(chan bool, 100)
	}
	if cfg.ForcePending == nil {
		cfg.ForcePending = &atomic.Bool{}
	}
	if cfg.SyncMu == nil {
		cfg.SyncMu = &sync.Mutex{}
	}
	if cfg.Immediate == nil {
		cfg.Immediate = make(chan struct{}, 1)
		startStandaloneAwakeNotifier(ctx, repoPath, cfg.Immediate)
	}

	for {
		missing := watchOnce(ctx, cfg, syncFn)
		if !missing || ctx.Err() != nil {
			break
		}
		if !WaitForRepoDir(ctx, repoPath, cfg.State, cfg.Emitter, repoCheckEvery) {
			break
		}
		cfg.InitialDelay = 0
		drainChannel(cfg.Trigger)
	}

	log.Printf("watch: stopped for repo=%s", repoPath)
	return nil
}

func watchOnce(ctx context.Context, cfg RepoConfig, syncFn func(context.Context, RepoConfig) error) bool {
	fsCtx, fsCancel := context.WithCancel(ctx)
	fsDone := make(chan struct{})
	go func() {
		defer close(fsDone)
		watchFileSystem(fsCtx, cfg)
	}()

	missing := newSyncScheduler(cfg, syncFn).run(ctx)
	fsCancel()
	<-fsDone
	return missing
}

func startStandaloneAwakeNotifier(ctx context.Context, repoPath string, out chan struct{}) {
	notifier, err := NewAwakeNotifier()
	if err != nil {
		log.Printf("awake: init error repo=%s err=%v", repoPath, err)
		return
	}
	wake := make(chan bool, 1)
	if err := notifier.Start(wake); err != nil {
		log.Printf("awake: start error repo=%s err=%v", repoPath, err)
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
}

func watchFileSystem(ctx context.Context, cfg RepoConfig) {
	repoPath := cfg.RepoPath
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		repoPath = resolved
	}
	notifyChannel := make(chan notify.EventInfo, 100)

	err := notify.Watch(filepath.Join(repoPath, "..."), notifyChannel, notify.Write, notify.Rename, notify.Remove, notify.Create)
	if err != nil {
		log.Printf("watch: file events unavailable repo=%s err=%v (polling only)", repoPath, err)
		return
	}
	defer notify.Stop(notifyChannel)

	for {
		select {
		case <-ctx.Done():
			return
		case ei, ok := <-notifyChannel:
			if !ok {
				return
			}
			path := ei.Path()
			ignore, err := ShouldIgnoreFile(repoPath, path)
			if err != nil {
				Debugf("watch: ignore-check error repo=%s path=%s err=%v", repoPath, path, err)
				if !RepoDirMissing(repoPath) {
					continue
				}
				ignore = false
			}
			if ignore {
				continue
			}

			Debugf("watch: event repo=%s op=%v path=%s", repoPath, ei.Event(), path)
			select {
			case cfg.Trigger <- true:
			default:
				Debugf("watch: trigger-channel-full repo=%s path=%s", repoPath, path)
			}
		}
	}
}

type syncScheduler struct {
	cfg    RepoConfig
	syncFn func(context.Context, RepoConfig) error

	cooldown          time.Duration
	errorBackoffMin   time.Duration
	errorBackoffMax   time.Duration
	offlineBackoffMin time.Duration
	offlineBackoffMax time.Duration
	existInterval     time.Duration

	errorBackoff   time.Duration
	offlineBackoff time.Duration
	holdUntil      time.Time
	cooldownUntil  time.Time
	missing        bool

	fsTimer  *time.Timer
	fsC      <-chan time.Time
	batching bool

	retryTimer *time.Timer
	retryC     <-chan time.Time
	retryLabel string
}

func newSyncScheduler(cfg RepoConfig, syncFn func(context.Context, RepoConfig) error) *syncScheduler {
	return &syncScheduler{
		cfg:               cfg,
		syncFn:            syncFn,
		cooldown:          syncCooldown,
		errorBackoffMin:   errorBackoffMin,
		errorBackoffMax:   errorBackoffMax,
		offlineBackoffMin: offlineBackoffMin,
		offlineBackoffMax: offlineBackoffMax,
		existInterval:     repoCheckEvery,
		errorBackoff:      errorBackoffMin,
		offlineBackoff:    offlineBackoffMin,
	}
}

func (s *syncScheduler) run(ctx context.Context) bool {
	pollTicker := time.NewTicker(s.cfg.PollInterval)
	defer pollTicker.Stop()
	existTicker := time.NewTicker(s.existInterval)
	defer existTicker.Stop()
	defer s.stopRetry()
	defer s.disarmFS()

	s.armRetry(s.cfg.InitialDelay, "initial")

	for !s.missing {
		select {
		case <-ctx.Done():
			return false
		case <-s.cfg.Immediate:
			s.fire(ctx, "immediate", s.cfg.ForcePending.Swap(false))
		case <-s.cfg.Trigger:
			if s.checkExists() {
				s.onFileEvent()
			}
		case <-s.fsC:
			label := "fs-event"
			if s.batching {
				label = "batch"
			}
			s.disarmFS()
			s.request(ctx, label, true)
		case <-s.retryC:
			label := s.retryLabel
			s.retryTimer = nil
			s.retryC = nil
			s.request(ctx, label, false)
		case <-pollTicker.C:
			s.request(ctx, "poll", false)
		case <-existTicker.C:
			s.checkExists()
		}
	}
	return ctx.Err() == nil
}

func (s *syncScheduler) checkExists() bool {
	if !RepoDirMissing(s.cfg.RepoPath) {
		return true
	}
	s.missing = true
	ReportRepoMissing(s.cfg.State, s.cfg.Emitter, s.cfg.RepoPath)
	return false
}

func (s *syncScheduler) onFileEvent() {
	if s.cfg.BatchWindow > 0 {
		s.resetFSTimer(s.cfg.BatchWindow)
		s.batching = true
		if s.cfg.State != nil {
			s.cfg.State.SetBatchPending(s.cfg.RepoPath, true, time.Now().Add(s.cfg.BatchWindow).Unix())
		}
		return
	}
	delay := s.cfg.FSLag
	if wait := time.Until(s.cooldownUntil); wait > 0 {
		delay += wait
	}
	s.resetFSTimer(delay)
}

func (s *syncScheduler) resetFSTimer(d time.Duration) {
	if s.fsTimer != nil {
		s.fsTimer.Stop()
	}
	s.fsTimer = time.NewTimer(d)
	s.fsC = s.fsTimer.C
}

func (s *syncScheduler) disarmFS() {
	if s.fsTimer != nil {
		s.fsTimer.Stop()
		s.fsTimer = nil
		s.fsC = nil
	}
	if s.batching {
		s.batching = false
		if s.cfg.State != nil {
			s.cfg.State.SetBatchPending(s.cfg.RepoPath, false, 0)
		}
	}
}

func (s *syncScheduler) armRetry(d time.Duration, label string) {
	if s.retryTimer != nil {
		return
	}
	if d < 0 {
		d = 0
	}
	s.retryTimer = time.NewTimer(d)
	s.retryC = s.retryTimer.C
	s.retryLabel = label
}

func (s *syncScheduler) stopRetry() {
	if s.retryTimer != nil {
		s.retryTimer.Stop()
		s.retryTimer = nil
		s.retryC = nil
	}
}

func (s *syncScheduler) request(ctx context.Context, label string, fileEvent bool) {
	if wait := time.Until(s.holdUntil); wait > 0 {
		if fileEvent && s.localCommitAllowed() {
			s.fireLocal(ctx, label)
		} else {
			Debugf("autosync: %s deferred repo=%s for=%s", label, s.cfg.RepoPath, wait.Round(time.Second))
		}
		s.armRetry(wait, label)
		return
	}
	s.fire(ctx, label, false)
}

func (s *syncScheduler) localCommitAllowed() bool {
	if s.cfg.State == nil {
		return true
	}
	st, ok := s.cfg.State.Get(s.cfg.RepoPath)
	return !ok || st.Status != StatusPreflight
}

func (s *syncScheduler) execute(ctx context.Context, run RepoConfig) (bool, error) {
	s.cfg.SyncMu.Lock()
	if ctx.Err() != nil {
		s.cfg.SyncMu.Unlock()
		return false, nil
	}
	err := s.syncFn(ctx, run)
	s.cfg.SyncMu.Unlock()

	s.cooldownUntil = time.Now().Add(s.cooldown)
	if ctx.Err() != nil {
		return false, err
	}
	if drainChannel(s.cfg.Trigger) {
		s.onFileEvent()
	}
	return true, err
}

func (s *syncScheduler) fireLocal(ctx context.Context, label string) {
	repoPath := s.cfg.RepoPath
	s.disarmFS()
	if s.cfg.isPaused() {
		Debugf("autosync: skip (paused) repo=%s trigger=%s", repoPath, label)
		return
	}
	if !s.checkExists() {
		return
	}

	run := s.cfg
	run.LocalOnly = true
	ran, err := s.execute(ctx, run)
	if !ran {
		return
	}
	if err != nil {
		log.Printf("autosync: %s local commit failed repo=%s err=%v", label, repoPath, err)
		return
	}
	Debugf("autosync: %s local commit done repo=%s", label, repoPath)
}

func (s *syncScheduler) fire(ctx context.Context, label string, force bool) {
	repoPath := s.cfg.RepoPath
	s.disarmFS()

	if s.cfg.isPaused() {
		Debugf("autosync: skip (paused) repo=%s trigger=%s", repoPath, label)
		return
	}
	if !s.checkExists() {
		return
	}
	s.stopRetry()

	run := s.cfg
	run.Force = force

	ran, err := s.execute(ctx, run)
	if !ran {
		return
	}

	now := time.Now()
	if err == nil {
		Debugf("autosync: %s success repo=%s force=%v", label, repoPath, force)
		s.errorBackoff = s.errorBackoffMin
		s.offlineBackoff = s.offlineBackoffMin
		s.holdUntil = time.Time{}
		return
	}

	if s.isOffline() {
		delay := s.offlineBackoff
		s.offlineBackoff = minDuration(s.offlineBackoff*2, s.offlineBackoffMax)
		s.errorBackoff = s.errorBackoffMin
		next := now.Add(delay)
		s.holdUntil = next
		if s.cfg.State != nil {
			s.cfg.State.SetNextRetry(repoPath, next.Unix())
		}
		log.Printf("autosync: %s offline repo=%s retry-in=%s err=%v", label, repoPath, delay, err)
		s.armRetry(delay, "offline-retry")
		return
	}

	if RepoDirMissing(repoPath) {
		s.checkExists()
		return
	}

	log.Printf("autosync: %s failed repo=%s err=%v", label, repoPath, err)
	s.offlineBackoff = s.offlineBackoffMin
	s.holdUntil = now.Add(s.errorBackoff)
	s.errorBackoff = minDuration(s.errorBackoff*2, s.errorBackoffMax)
}

func (s *syncScheduler) isOffline() bool {
	if s.cfg.State == nil {
		return false
	}
	st, ok := s.cfg.State.Get(s.cfg.RepoPath)
	return ok && st.Status == StatusOffline
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func drainChannel(ch chan bool) bool {
	drained := false
	for {
		select {
		case <-ch:
			drained = true
		default:
			return drained
		}
	}
}
