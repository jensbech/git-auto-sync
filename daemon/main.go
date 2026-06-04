package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"github.com/kardianos/service"
)

type watcherHandle struct {
	cancel  context.CancelFunc
	trigger chan bool
	paused  *atomic.Bool
}

type Daemon struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	watchers map[string]*watcherHandle
	wg       sync.WaitGroup
	emitter  common.EventEmitter
	sock     *common.SocketServer
	state    *common.RepoStateStore
}

func (d *Daemon) Start(s service.Service) error {
	go d.run()
	return nil
}

func (d *Daemon) run() {
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx = ctx
	d.cancel = cancel
	d.watchers = make(map[string]*watcherHandle)
	d.state = common.NewRepoStateStore()
	d.emitter = common.NewLogEmitter()

	socketPath := common.DefaultSocketPath()
	sock, err := common.NewSocketServer(socketPath, d)
	if err != nil {
		log.Printf("daemon: socket server failed to start: %v (continuing without socket)", err)
	} else {
		d.sock = sock
		d.emitter = common.NewMultiEmitter(common.NewLogEmitter(), sock)

		sock.SetWelcomeProvider(func() interface{} {
			return map[string]interface{}{
				"type":   "hello",
				"daemon": "running",
				"repos":  d.state.Snapshot(),
			}
		})

		d.state.OnChange(func(rs common.RepoState) {
			sock.Broadcast(map[string]interface{}{
				"type": "state",
				"repo": rs.Path,
				"ts":   time.Now().Unix(),
				"data": rs,
			})
		})

		go sock.Accept()
	}

	config, err := d.readConfigWithRetry()
	if err != nil {
		log.Printf("daemon: failed to read config after retries: %v", err)
		cancel()
		return
	}

	for _, rp := range config.Repos {
		d.state.Ensure(rp)
	}

	d.startWatchers(ctx, config)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			log.Println("daemon: received SIGHUP, reloading config")
			newConfig, err := cfg.Read()
			if err != nil {
				log.Printf("daemon: failed to reload config: %v", err)
				continue
			}
			d.reconcileWatchers(ctx, newConfig)
		}
	}
}

func (d *Daemon) readConfigWithRetry() (*cfg.Config, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		config, err := cfg.Read()
		if err == nil {
			return config, nil
		}
		lastErr = err
		log.Printf("daemon: config read attempt %d/3 failed: %v", attempt, err)
		if attempt < 3 {
			time.Sleep(5 * time.Second)
		}
	}
	return nil, lastErr
}

func (d *Daemon) startWatchers(ctx context.Context, config *cfg.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, repoPath := range config.Repos {
		if _, exists := d.watchers[repoPath]; exists {
			continue
		}
		d.startWatcher(ctx, repoPath, config)
	}
}

func (d *Daemon) startWatcher(ctx context.Context, repoPath string, config *cfg.Config) {
	watchCtx, watchCancel := context.WithCancel(ctx)
	handle := &watcherHandle{
		cancel:  watchCancel,
		trigger: make(chan bool, 100),
		paused:  &atomic.Bool{},
	}
	d.watchers[repoPath] = handle
	d.state.Ensure(repoPath)

	d.wg.Add(1)
	log.Printf("daemon: monitoring %s", repoPath)
	go func() {
		defer d.wg.Done()

		repoCfg, err := common.NewRepoConfig(repoPath)
		if err != nil {
			log.Printf("daemon: config error for %s: %v", repoPath, err)
			d.state.MarkError(repoPath, "config error: "+err.Error())
			return
		}

		repoCfg.Env = append(repoCfg.Env, config.Envs...)
		repoCfg.Emitter = d.emitter
		repoCfg.Clients = d.sock
		repoCfg.State = d.state
		repoCfg.Trigger = handle.trigger
		repoCfg.Paused = handle.paused

		if err := common.WatchForChanges(watchCtx, repoCfg); err != nil {
			log.Printf("daemon: watcher error for %s: %v", repoPath, err)
			d.state.MarkError(repoPath, "watcher error: "+err.Error())
		}
	}()
}

func (d *Daemon) reconcileWatchers(ctx context.Context, config *cfg.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()

	desired := make(map[string]bool)
	for _, rp := range config.Repos {
		desired[rp] = true
	}

	for rp, h := range d.watchers {
		if !desired[rp] {
			log.Printf("daemon: stopping watcher for removed repo %s", rp)
			h.cancel()
			delete(d.watchers, rp)
			d.state.Remove(rp)
		}
	}

	for _, rp := range config.Repos {
		if _, exists := d.watchers[rp]; !exists {
			d.startWatcher(ctx, rp, config)
		}
	}
}

func (d *Daemon) restartWatcher(repoPath string) {
	d.mu.Lock()
	handle, ok := d.watchers[repoPath]
	if !ok {
		d.mu.Unlock()
		return
	}
	handle.cancel()
	delete(d.watchers, repoPath)
	d.mu.Unlock()

	config, err := cfg.Read()
	if err != nil {
		log.Printf("daemon: restart-watcher config read failed: %v", err)
		return
	}

	if d.ctx == nil || d.ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	d.startWatcher(d.ctx, repoPath, config)
	d.mu.Unlock()
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
	case <-time.After(10 * time.Second):
		log.Println("daemon: timed out waiting for watchers to stop")
	}

	if d.sock != nil {
		d.sock.Close()
	}
	return nil
}

func (d *Daemon) HandleCommand(cmd common.SocketCommand) (interface{}, error) {
	switch cmd.Cmd {
	case "list", "status":
		repos := d.state.Snapshot()
		return map[string]interface{}{
			"type":   "status",
			"daemon": "running",
			"repos":  repos,
		}, nil

	case "add":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		config, err := cfg.Read()
		if err != nil {
			return nil, err
		}
		for _, rp := range config.Repos {
			if rp == cmd.Repo {
				return map[string]string{"status": "already_exists"}, nil
			}
		}
		config.Repos = append(config.Repos, cmd.Repo)
		if err := cfg.Write(config); err != nil {
			return nil, err
		}
		d.state.Ensure(cmd.Repo)
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
		return map[string]string{"status": "added"}, nil

	case "remove":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		config, err := cfg.Read()
		if err != nil {
			return nil, err
		}
		found := false
		repos := make([]string, 0, len(config.Repos))
		for _, rp := range config.Repos {
			if rp == cmd.Repo {
				found = true
				continue
			}
			repos = append(repos, rp)
		}
		if !found {
			return map[string]string{"status": "not_found"}, nil
		}
		config.Repos = repos
		if err := cfg.Write(config); err != nil {
			return nil, err
		}
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
		return map[string]string{"status": "removed"}, nil

	case "sync_now":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		d.mu.Lock()
		handle, ok := d.watchers[cmd.Repo]
		d.mu.Unlock()
		if !ok {
			return map[string]string{"status": "not_watching"}, nil
		}
		select {
		case handle.trigger <- true:
		default:
		}
		return map[string]string{"status": "triggered"}, nil

	case "pause":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		d.mu.Lock()
		handle, ok := d.watchers[cmd.Repo]
		d.mu.Unlock()
		if !ok {
			return map[string]string{"status": "not_watching"}, nil
		}
		handle.paused.Store(true)
		d.state.SetPaused(cmd.Repo, true)
		return map[string]string{"status": "paused"}, nil

	case "resume":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		d.mu.Lock()
		handle, ok := d.watchers[cmd.Repo]
		d.mu.Unlock()
		if !ok {
			return map[string]string{"status": "not_watching"}, nil
		}
		handle.paused.Store(false)
		d.state.SetPaused(cmd.Repo, false)
		select {
		case handle.trigger <- true:
		default:
		}
		return map[string]string{"status": "resumed"}, nil

	case "get_settings":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		return d.readRepoSettings(cmd.Repo)

	case "set_settings":
		if cmd.Repo == "" {
			return map[string]string{"error": "missing repo"}, nil
		}
		if err := d.writeRepoSettings(cmd.Repo, cmd.Settings); err != nil {
			return map[string]string{"error": err.Error()}, nil
		}
		d.restartWatcher(cmd.Repo)
		return map[string]string{"status": "saved"}, nil
	}

	return map[string]string{"error": "unknown command"}, nil
}

func (d *Daemon) readRepoSettings(repoPath string) (map[string]interface{}, error) {
	out := map[string]interface{}{
		"repo": repoPath,
	}
	for key, alias := range map[string]string{
		"syncInterval": "pollInterval",
		"batchWindow":  "batchWindow",
		"exec":         "exec",
	} {
		cmd := exec.Command("git", "config", "--get", "auto-sync."+key)
		cmd.Dir = repoPath
		buf, err := cmd.Output()
		val := strings.TrimSpace(string(buf))
		if err == nil && val != "" {
			if n, err := strconv.Atoi(val); err == nil {
				out[alias] = n
			} else {
				out[alias] = val
			}
		} else {
			out[alias] = nil
		}
	}
	return out, nil
}

func (d *Daemon) writeRepoSettings(repoPath string, settings map[string]string) error {
	allowed := map[string]string{
		"pollInterval": "syncInterval",
		"batchWindow":  "batchWindow",
		"exec":         "exec",
	}
	for k, v := range settings {
		gitKey, ok := allowed[k]
		if !ok {
			continue
		}
		cmd := exec.Command("git", "config", "auto-sync."+gitKey, v)
		cmd.Dir = repoPath
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	daemon := &Daemon{}
	autoSyncService, err := common.NewServiceWithDaemon(daemon)
	if err != nil {
		log.Fatal("BuildService", err)
	}

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
