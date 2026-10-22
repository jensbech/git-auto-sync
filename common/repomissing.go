package common

import (
	"context"
	"errors"
	"log"
	"os"
	"syscall"
	"time"
)

const RepoCheckInterval = 10 * time.Second

func RepoMissingMessage(path string) string {
	return "Repository folder not found: " + path
}

func RepoDirMissing(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return os.IsNotExist(err) || isNotDirErr(err)
	}
	return !info.IsDir()
}

func isNotDirErr(err error) bool {
	return errors.Is(err, syscall.ENOTDIR)
}

func ReportRepoMissing(state *RepoStateStore, emitter EventEmitter, path string) {
	msg := RepoMissingMessage(path)
	changed := true
	if state != nil {
		changed = state.MarkRepoMissing(path, msg)
	}
	if !changed {
		return
	}
	log.Printf("watch: repository folder not found repo=%s (waiting for it to come back)", path)
	if emitter != nil {
		emitter.Emit(NewCodedEvent(EventPreflight, path, msg, CodeRepoMissing))
	}
}

func ReportRepoFound(state *RepoStateStore, emitter EventEmitter, path string) {
	if state == nil || !state.ClearRepoMissing(path) {
		return
	}
	log.Printf("watch: repository folder is back repo=%s", path)
	if emitter != nil {
		emitter.Emit(NewEvent(EventRecovered, path, "repository folder found again", StatusIdle))
	}
}

func WaitForRepoDir(ctx context.Context, path string, state *RepoStateStore, emitter EventEmitter, interval time.Duration) bool {
	if !RepoDirMissing(path) {
		ReportRepoFound(state, emitter, path)
		return ctx.Err() == nil
	}
	ReportRepoMissing(state, emitter, path)
	if interval <= 0 {
		interval = RepoCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if !RepoDirMissing(path) {
				ReportRepoFound(state, emitter, path)
				return ctx.Err() == nil
			}
		}
	}
}
