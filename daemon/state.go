package main

import (
	"context"
	"log"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
)

const statePersistDebounce = 2 * time.Second

func (d *Daemon) ensureRestored(repos []string) {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	if d.stateReady {
		return
	}
	d.stateReady = true

	keep := make(map[string]bool, len(repos))
	for _, rp := range repos {
		keep[rp] = true
		d.state.Ensure(rp)
	}
	file := common.StateFilePath()
	n, err := d.state.LoadPersisted(file, func(p string) bool { return keep[p] })
	if err != nil {
		log.Printf("daemon: warning: state file %s is unreadable, starting with fresh repo state: %v", file, err)
		return
	}
	if n > 0 {
		log.Printf("daemon: restored state for %d repo(s)", n)
	}
}

func (d *Daemon) requestPersist() {
	select {
	case d.persistCh <- struct{}{}:
	default:
	}
}

func (d *Daemon) startStatePersistence(ctx context.Context) {
	d.state.OnChange(func(common.RepoState) {
		d.requestPersist()
	})

	go func() {
		var timer *time.Timer
		var timerC <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-d.persistCh:
				if timer == nil {
					timer = time.NewTimer(statePersistDebounce)
					timerC = timer.C
				}
			case <-timerC:
				timer = nil
				timerC = nil
				d.saveState()
			}
		}
	}()
}

func (d *Daemon) saveState() {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	if !d.stateReady {
		return
	}
	data, err := d.state.SavePersisted(common.StateFilePath(), d.persisted)
	if err != nil {
		log.Printf("daemon: cannot save repo state: %v", err)
		return
	}
	d.persisted = data
}
