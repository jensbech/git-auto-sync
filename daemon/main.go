package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"github.com/kardianos/service"
)

type Daemon struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	watchers map[string]context.CancelFunc
	wg       sync.WaitGroup
	emitter  common.EventEmitter
	sock     *common.SocketServer
}

func (d *Daemon) Start(s service.Service) error {
	go d.run()
	return nil
}

func (d *Daemon) run() {
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.watchers = make(map[string]context.CancelFunc)
	d.emitter = common.NewLogEmitter()

	socketPath := common.DefaultSocketPath()
	sock, err := common.NewSocketServer(socketPath, d)
	if err != nil {
		log.Printf("daemon: socket server failed to start: %v (continuing without socket)", err)
	} else {
		d.sock = sock
		d.emitter = common.NewMultiEmitter(common.NewLogEmitter(), sock)
		go sock.Accept()
	}

	config, err := d.readConfigWithRetry()
	if err != nil {
		log.Printf("daemon: failed to read config after retries: %v", err)
		cancel()
		return
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
	d.watchers[repoPath] = watchCancel

	d.wg.Add(1)
	log.Printf("daemon: monitoring %s", repoPath)
	go func() {
		defer d.wg.Done()

		repoCfg, err := common.NewRepoConfig(repoPath)
		if err != nil {
			log.Printf("daemon: config error for %s: %v", repoPath, err)
			return
		}

		repoCfg.Env = append(repoCfg.Env, config.Envs...)
		repoCfg.Emitter = d.emitter
		repoCfg.Clients = d.sock

		if err := common.WatchForChanges(watchCtx, repoCfg); err != nil {
			log.Printf("daemon: watcher error for %s: %v", repoPath, err)
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

	for rp, cancel := range d.watchers {
		if !desired[rp] {
			log.Printf("daemon: stopping watcher for removed repo %s", rp)
			cancel()
			delete(d.watchers, rp)
		}
	}

	for _, rp := range config.Repos {
		if _, exists := d.watchers[rp]; !exists {
			d.startWatcher(ctx, rp, config)
		}
	}
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
		config, err := cfg.Read()
		if err != nil {
			return nil, err
		}
		type repoInfo struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		}
		repos := make([]repoInfo, 0, len(config.Repos))
		for _, rp := range config.Repos {
			repos = append(repos, repoInfo{Path: rp, Status: "ok"})
		}
		return map[string]interface{}{
			"type":   "status",
			"repos":  repos,
			"daemon": "running",
		}, nil

	case "add":
		if cmd.Repo == "" {
			return nil, nil
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
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
		return map[string]string{"status": "added"}, nil

	case "remove":
		if cmd.Repo == "" {
			return nil, nil
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
	}

	return map[string]string{"error": "unknown command"}, nil
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
