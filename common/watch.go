package common

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/rjeczalik/notify"
	"github.com/ztrue/tracerr"
	git "gopkg.in/src-d/go-git.v4"
)

type ClientChecker interface {
	HasClients() bool
}

type RepoConfig struct {
	RepoPath     string
	PollInterval time.Duration
	FSLag        time.Duration
	BatchWindow  time.Duration
	GitExec      string
	Env          []string
	Emitter      EventEmitter
	Clients      ClientChecker
	State        *RepoStateStore
	Trigger      chan bool
	Paused       *atomic.Bool
}

type AwakeNotifier interface {
	Start(chan bool) error
}

func NewRepoConfig(repoPath string) (RepoConfig, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return RepoConfig{}, tracerr.Wrap(err)
	}

	config, err := repo.Config()
	if err != nil {
		return RepoConfig{}, tracerr.Wrap(err)
	}

	autoSyncSection := config.Raw.Section("auto-sync")

	pollInterval := 10 * time.Minute
	if autoSyncSection.Option("syncInterval") != "" {
		secondsStr := autoSyncSection.Option("syncInterval")
		seconds, err := strconv.Atoi(secondsStr)
		if err != nil {
			return RepoConfig{}, tracerr.Wrap(err)
		}

		pollInterval = time.Duration(seconds) * time.Second
	}

	batchWindow := time.Duration(0)
	if autoSyncSection.Option("batchWindow") != "" {
		secondsStr := autoSyncSection.Option("batchWindow")
		seconds, err := strconv.Atoi(secondsStr)
		if err != nil {
			return RepoConfig{}, tracerr.Wrap(err)
		}
		batchWindow = time.Duration(seconds) * time.Second
	}

	gitExec := ""
	if autoSyncSection.Option("exec") != "" {
		gitExec = autoSyncSection.Option("exec")

		_, err := os.Stat(gitExec)
		if err != nil {
			return RepoConfig{}, tracerr.Wrap(err)
		}
	}

	return RepoConfig{
		RepoPath:     repoPath,
		PollInterval: pollInterval,
		FSLag:        1 * time.Second,
		BatchWindow:  batchWindow,
		GitExec:      gitExec,
	}, nil
}

func (cfg RepoConfig) isPaused() bool {
	if cfg.Paused == nil {
		return false
	}
	return cfg.Paused.Load()
}

func WatchForChanges(ctx context.Context, cfg RepoConfig) error {
	repoPath := cfg.RepoPath
	log.Printf("watch: starting for repo=%s", repoPath)

	if cfg.State != nil {
		cfg.State.SetWatching(repoPath, true)
		cfg.State.SetSettings(repoPath, int(cfg.PollInterval/time.Second), int(cfg.BatchWindow/time.Second))
		defer cfg.State.SetWatching(repoPath, false)
	}

	runSync := func(trigger string) {
		if cfg.isPaused() {
			log.Printf("autosync: skip (paused) repo=%s trigger=%s", repoPath, trigger)
			return
		}
		if err := AutoSync(cfg); err != nil {
			log.Printf("autosync: %s failed repo=%s err=%v", trigger, repoPath, err)
		} else {
			log.Printf("autosync: %s success repo=%s", trigger, repoPath)
		}
	}

	runSync("initial")

	trigger := cfg.Trigger
	if trigger == nil {
		trigger = make(chan bool, 100)
	}
	pollTicker := time.NewTicker(cfg.PollInterval)

	go func() {
		defer pollTicker.Stop()

		notifier, err := NewAwakeNotifier()
		if err != nil {
			log.Printf("awake: init error repo=%s err=%v", repoPath, err)
		} else {
			if err := notifier.Start(trigger); err != nil {
				log.Printf("awake: start error repo=%s err=%v", repoPath, err)
			}
		}

		backoff := 1 * time.Second
		maxBackoff := 60 * time.Second
		cooldown := 5 * time.Second

		var batchTimer *time.Timer
		var batchTimerC <-chan time.Time

		armBatch := func() {
			if cfg.BatchWindow <= 0 {
				return
			}
			due := time.Now().Add(cfg.BatchWindow)
			if batchTimer != nil {
				batchTimer.Stop()
			}
			batchTimer = time.NewTimer(cfg.BatchWindow)
			batchTimerC = batchTimer.C
			if cfg.State != nil {
				cfg.State.SetBatchPending(repoPath, true, due.Unix())
			}
		}
		disarmBatch := func() {
			if batchTimer != nil {
				batchTimer.Stop()
				batchTimer = nil
				batchTimerC = nil
			}
			if cfg.State != nil {
				cfg.State.SetBatchPending(repoPath, false, 0)
			}
		}

		fire := func(label string) {
			if cfg.isPaused() {
				log.Printf("autosync: skip (paused) repo=%s trigger=%s", repoPath, label)
				return
			}
			time.Sleep(cfg.FSLag)
			if err := AutoSync(cfg); err != nil {
				log.Printf("autosync: %s failed repo=%s err=%v", label, repoPath, err)
				time.Sleep(backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			} else {
				log.Printf("autosync: %s success repo=%s backoff-reset", label, repoPath)
				backoff = 1 * time.Second
			}
			drainChannel(trigger)
			time.Sleep(cooldown)
			drainChannel(trigger)
		}

		for {
			select {
			case <-ctx.Done():
				disarmBatch()
				return
			case <-trigger:
				if cfg.BatchWindow > 0 {
					armBatch()
				} else {
					fire("fs-event")
				}
			case <-batchTimerC:
				disarmBatch()
				fire("batch")
			case <-pollTicker.C:
				disarmBatch()
				fire("poll")
			}
		}
	}()

	notifyChannel := make(chan notify.EventInfo, 100)

	err := notify.Watch(filepath.Join(repoPath, "..."), notifyChannel, notify.Write, notify.Rename, notify.Remove, notify.Create)
	if err != nil {
		return tracerr.Wrap(err)
	}
	defer notify.Stop(notifyChannel)

	for {
		select {
		case <-ctx.Done():
			log.Printf("watch: stopping for repo=%s", repoPath)
			return nil
		case ei, ok := <-notifyChannel:
			if !ok {
				return nil
			}
			path := ei.Path()
			ignore, err := ShouldIgnoreFile(repoPath, path)
			if err != nil {
				log.Printf("watch: ignore-check error repo=%s path=%s err=%v", repoPath, path, err)
				continue
			}
			if ignore {
				continue
			}

			log.Printf("watch: event repo=%s op=%v path=%s", repoPath, ei.Event(), path)
			select {
			case trigger <- true:
			default:
				log.Printf("watch: filtered-channel-full repo=%s path=%s", repoPath, path)
			}
		}
	}
}

func drainChannel(ch chan bool) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
