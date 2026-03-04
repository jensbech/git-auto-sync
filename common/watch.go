package common

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
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
	GitExec      string
	Env          []string
	Emitter      EventEmitter
	Clients      ClientChecker
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
		GitExec:      gitExec,
	}, nil
}

func WatchForChanges(ctx context.Context, cfg RepoConfig) error {
	repoPath := cfg.RepoPath
	log.Printf("watch: starting for repo=%s", repoPath)

	if err := AutoSync(cfg); err != nil {
		log.Printf("watch: initial autosync error repo=%s err=%v", repoPath, err)
	}

	notifyFilteredChannel := make(chan bool, 100)
	pollTicker := time.NewTicker(cfg.PollInterval)

	go func() {
		defer pollTicker.Stop()

		notifier, err := NewAwakeNotifier()
		if err != nil {
			log.Printf("awake: init error repo=%s err=%v", repoPath, err)
		} else {
			if err := notifier.Start(notifyFilteredChannel); err != nil {
				log.Printf("awake: start error repo=%s err=%v", repoPath, err)
			}
		}

		backoff := 1 * time.Second
		maxBackoff := 60 * time.Second

		cooldown := 5 * time.Second

		for {
			select {
			case <-ctx.Done():
				return
			case <-notifyFilteredChannel:
				time.Sleep(cfg.FSLag)
				if err := AutoSync(cfg); err != nil {
					log.Printf("autosync: fs-event failed repo=%s err=%v", repoPath, err)
					time.Sleep(backoff)
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				} else {
					log.Printf("autosync: fs-event success repo=%s backoff-reset", repoPath)
					backoff = 1 * time.Second
				}
				drainChannel(notifyFilteredChannel)
				time.Sleep(cooldown)
				drainChannel(notifyFilteredChannel)
			case <-pollTicker.C:
				if err := AutoSync(cfg); err != nil {
					log.Printf("autosync: poll failed repo=%s err=%v", repoPath, err)
				} else {
					log.Printf("autosync: poll success repo=%s", repoPath)
				}
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
			case notifyFilteredChannel <- true:
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
