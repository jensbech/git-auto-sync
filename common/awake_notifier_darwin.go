package common

import (
	"errors"
	"sync"

	"github.com/prashantgupta24/mac-sleep-notifier/notifier"
)

var darwinAwakeOnce sync.Once

type AwakeNotifierDarwin struct {
	n *notifier.Notifier
}

func NewAwakeNotifier() (AwakeNotifier, error) {
	return &AwakeNotifierDarwin{n: notifier.GetInstance()}, nil
}

func (a *AwakeNotifierDarwin) Start(out chan bool) error {
	started := false
	darwinAwakeOnce.Do(func() {
		started = true
		activities := a.n.Start()
		go func() {
			for activity := range activities {
				if activity.Type != notifier.Awake {
					continue
				}
				select {
				case out <- true:
				default:
				}
			}
		}()
	})
	if !started {
		return errors.New("awake notifier already started in this process")
	}
	return nil
}
