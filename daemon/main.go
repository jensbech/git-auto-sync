package main

import (
	"context"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"github.com/kardianos/service"
)

const (
	initialSyncStagger = 3 * time.Second
	stopTimeout        = 10 * time.Second
)

type watcherHandle struct {
	cancel    context.CancelFunc
	done      chan struct{}
	trigger   chan bool
	immediate chan struct{}
	force     *atomic.Bool
	paused    *atomic.Bool
}

type watchFunc func(ctx context.Context, repoPath string, h *watcherHandle, envs []string, initialDelay time.Duration)

type syncFunc func(ctx context.Context, cfg common.RepoConfig) error

type Daemon struct {
	mu        sync.Mutex
	cfgMu     sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	watchers  map[string]*watcherHandle
	stopping  map[string]chan struct{}
	syncLocks map[string]*sync.Mutex
	envs      []string
	wg        sync.WaitGroup
	emitter   common.EventEmitter
	sock      *common.SocketServer
	state     *common.RepoStateStore
	watchFn   watchFunc
	syncFn    syncFunc

	repoCheckInterval time.Duration
	netFingerprint    func() string
	netInterval       time.Duration
	configRetryDelay  time.Duration

	persistMu  sync.Mutex
	persisted  []byte
	stateReady bool
	persistCh  chan struct{}
}

func (d *Daemon) init() {
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx = ctx
	d.cancel = cancel
	d.watchers = make(map[string]*watcherHandle)
	d.stopping = make(map[string]chan struct{})
	d.syncLocks = make(map[string]*sync.Mutex)
	d.state = common.NewRepoStateStore()
	d.emitter = common.NewLogEmitter()
	d.persistCh = make(chan struct{}, 1)
}

func (d *Daemon) Start(s service.Service) error {
	d.init()
	go d.run()
	return nil
}

func (d *Daemon) run() {
	ctx := d.ctx

	reload, stopReload := notifyReload()
	defer stopReload()

	d.startSocket()

	config, err := d.readConfigWithRetry(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("daemon: cannot read config: %v; no repos are monitored until the file is fixed (then send SIGHUP or restart the daemon)", err)
	}

	d.startStatePersistence(ctx)
	if config != nil {
		d.startWatchers(ctx, config)
	}
	d.startWakeBroadcaster(ctx)
	d.startNetworkWatcher(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-reload:
			d.reloadConfig()
		}
	}
}

func (d *Daemon) reloadConfig() {
	log.Println("daemon: reloading config")
	newConfig, err := cfg.Read()
	if err != nil {
		log.Printf("daemon: failed to reload config, keeping the current repos: %v", err)
		return
	}
	d.reconcileWatchers(d.ctx, newConfig)
}

func (d *Daemon) readConfigWithRetry(ctx context.Context) (*cfg.Config, error) {
	retryDelay := d.configRetryDelay
	if retryDelay <= 0 {
		retryDelay = 5 * time.Second
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		config, err := cfg.Read()
		if err == nil {
			return config, nil
		}
		lastErr = err
		log.Printf("daemon: config read attempt %d/3 failed: %v", attempt, err)
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return nil, lastErr
}

func (d *Daemon) startWakeBroadcaster(ctx context.Context) {
	notifier, err := common.NewAwakeNotifier()
	if err != nil {
		log.Printf("daemon: awake notifier init failed: %v", err)
		return
	}
	wake := make(chan bool, 1)
	if err := notifier.Start(wake); err != nil {
		log.Printf("daemon: awake notifier start failed: %v", err)
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				log.Println("daemon: woke from sleep, syncing all repos")
				d.SyncAll()
			}
		}
	}()
}

func uniqueRepos(repos []string) []string {
	out := make([]string, 0, len(repos))
	seen := make(map[string]string, len(repos))
	for _, rp := range repos {
		key := common.ResolvePath(rp)
		if first, dup := seen[key]; dup {
			log.Printf("daemon: ignoring duplicate config entry %s (same folder as %s)", rp, first)
			continue
		}
		seen[key] = rp
		out = append(out, rp)
	}
	return out
}

func (d *Daemon) startWatchers(ctx context.Context, config *cfg.Config) {
	repos := uniqueRepos(config.Repos)
	d.ensureRestored(repos)
	d.mu.Lock()
	defer d.mu.Unlock()

	d.envs = append([]string(nil), config.Envs...)
	n := 0
	for _, repoPath := range repos {
		if _, exists := d.watchers[repoPath]; exists {
			continue
		}
		d.startWatcherLocked(ctx, repoPath, time.Duration(n)*initialSyncStagger)
		n++
	}
}

func (d *Daemon) syncLockLocked(repoPath string) *sync.Mutex {
	m, ok := d.syncLocks[repoPath]
	if !ok {
		m = &sync.Mutex{}
		d.syncLocks[repoPath] = m
	}
	return m
}

func (d *Daemon) syncLock(repoPath string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.syncLockLocked(repoPath)
}

func (d *Daemon) startWatcherLocked(ctx context.Context, repoPath string, initialDelay time.Duration) {
	watchCtx, watchCancel := context.WithCancel(ctx)
	handle := &watcherHandle{
		cancel:    watchCancel,
		done:      make(chan struct{}),
		trigger:   make(chan bool, 100),
		immediate: make(chan struct{}, 1),
		force:     &atomic.Bool{},
		paused:    &atomic.Bool{},
	}
	d.state.Ensure(repoPath)
	if st, ok := d.state.Get(repoPath); ok {
		handle.paused.Store(st.Paused)
	}

	prev := d.stopping[repoPath]
	delete(d.stopping, repoPath)
	d.watchers[repoPath] = handle
	envs := append([]string(nil), d.envs...)
	watch := d.watchFn
	if watch == nil {
		watch = d.runWatcher
	}

	d.wg.Add(1)
	log.Printf("daemon: monitoring %s", repoPath)
	go func() {
		defer d.wg.Done()
		defer close(handle.done)

		if prev != nil {
			<-prev
		}
		if watchCtx.Err() != nil {
			return
		}
		watch(watchCtx, repoPath, handle, envs, initialDelay)
	}()
}

func (d *Daemon) checkInterval() time.Duration {
	if d.repoCheckInterval > 0 {
		return d.repoCheckInterval
	}
	return common.RepoCheckInterval
}

func (d *Daemon) runWatcher(ctx context.Context, repoPath string, h *watcherHandle, envs []string, initialDelay time.Duration) {
	var repoCfg common.RepoConfig
	for {
		if !common.WaitForRepoDir(ctx, repoPath, d.state, d.emitter, d.checkInterval()) {
			return
		}
		var err error
		repoCfg, err = common.NewRepoConfig(repoPath)
		if err == nil {
			break
		}
		if common.RepoDirMissing(repoPath) {
			continue
		}
		log.Printf("daemon: config error for %s: %v", repoPath, err)
		if ctx.Err() == nil {
			d.state.MarkErrorCode(repoPath, common.CodeConfig, common.ErrorMessage(err))
		}
		return
	}

	repoCfg.Env = append(repoCfg.Env, envs...)
	repoCfg.Emitter = d.emitter
	if d.sock != nil {
		repoCfg.Clients = d.sock
	}
	repoCfg.State = d.state
	repoCfg.Trigger = h.trigger
	repoCfg.Paused = h.paused
	repoCfg.Immediate = h.immediate
	repoCfg.ForcePending = h.force
	repoCfg.SyncMu = d.syncLock(repoPath)
	repoCfg.InitialDelay = initialDelay

	common.RestoreBranchInfo(ctx, repoCfg)

	syncFn := d.syncFn
	if syncFn == nil {
		syncFn = common.AutoSyncCtx
	}
	if err := common.WatchForChangesWith(ctx, repoCfg, syncFn); err != nil {
		log.Printf("daemon: watcher error for %s: %v", repoPath, err)
		if ctx.Err() == nil {
			d.state.MarkError(repoPath, "File watcher stopped: "+common.ErrorMessage(err))
		}
	}
}

func (d *Daemon) stopWatcherLocked(repoPath string) *watcherHandle {
	h, ok := d.watchers[repoPath]
	if !ok {
		return nil
	}
	h.cancel()
	delete(d.watchers, repoPath)
	d.stopping[repoPath] = h.done
	go func() {
		<-h.done
		d.mu.Lock()
		if d.stopping[repoPath] == h.done {
			delete(d.stopping, repoPath)
		}
		d.mu.Unlock()
	}()
	return h
}

func (d *Daemon) reconcileWatchers(ctx context.Context, config *cfg.Config) {
	if ctx == nil || ctx.Err() != nil {
		return
	}

	repos := uniqueRepos(config.Repos)
	d.ensureRestored(repos)
	d.mu.Lock()
	defer d.mu.Unlock()

	envsChanged := !slices.Equal(d.envs, config.Envs)
	d.envs = append([]string(nil), config.Envs...)

	desired := make(map[string]bool)
	for _, rp := range repos {
		desired[rp] = true
	}

	var restart []string
	for rp := range d.watchers {
		if desired[rp] {
			if envsChanged {
				restart = append(restart, rp)
			}
			continue
		}
		log.Printf("daemon: stopping watcher for removed repo %s", rp)
		h := d.stopWatcherLocked(rp)
		d.state.Remove(rp)
		d.requestPersist()
		go d.forgetWhenStopped(rp, h)
	}

	if len(restart) > 0 {
		log.Printf("daemon: environment changed, restarting %d watcher(s)", len(restart))
	}
	for _, rp := range restart {
		d.stopWatcherLocked(rp)
		d.startWatcherLocked(ctx, rp, 0)
	}

	for _, rp := range repos {
		if _, exists := d.watchers[rp]; !exists {
			d.startWatcherLocked(ctx, rp, 0)
		}
	}
}

func (d *Daemon) forgetWhenStopped(repoPath string, h *watcherHandle) {
	<-h.done
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, back := d.watchers[repoPath]; back {
		return
	}
	d.state.Remove(repoPath)
	delete(d.syncLocks, repoPath)
	d.requestPersist()
}

func (d *Daemon) restartWatcher(repoPath string) {
	key, _, ok := d.lookupWatcher(repoPath)
	if !ok {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil || d.ctx.Err() != nil {
		return
	}
	if _, ok := d.watchers[key]; !ok {
		return
	}
	d.stopWatcherLocked(key)
	d.startWatcherLocked(d.ctx, key, 0)
}

func (d *Daemon) Stop(s service.Service) error {
	if d.cancel != nil {
		d.cancel()
	}

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(stopTimeout):
		log.Println("daemon: timed out waiting for watchers to stop")
	}

	if d.state != nil {
		d.saveState()
	}

	if d.sock != nil {
		d.sock.Close()
	}
	return nil
}

func main() {
	daemon := &Daemon{}
	autoSyncService, err := common.NewServiceWithDaemon(daemon)
	if err != nil {
		log.Fatal("BuildService", err)
	}

	setupLogging(service.Interactive())

	s := autoSyncService.Service
	logger, err := s.Logger(nil)
	if err != nil {
		log.Fatal("BuildLogger", err)
	}

	err = s.Run()
	if err != nil {
		_ = logger.Error("RunService", err)
	}
}
