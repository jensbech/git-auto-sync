//go:build !windows

package main

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	"github.com/kirsle/configdir"
	"gotest.tools/v3/assert"
)

func shortSocket(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gas-a2-")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("GIT_AUTO_SYNC_SOCKET", filepath.Join(dir, "d.sock"))
}

func writeConfigByHand(t *testing.T, content string) {
	t.Helper()
	dir := configdir.LocalConfig("git-auto-sync")
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	f, err := os.OpenFile(filepath.Join(dir, "config.json"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	assert.NilError(t, err)
	_, err = f.WriteString(content)
	assert.NilError(t, err)
	assert.NilError(t, f.Close())
}

func hupUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		assert.NilError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
		for i := 0; i < 10; i++ {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatal("condition not met after SIGHUP")
}

func startRun(t *testing.T, d *Daemon) {
	t.Helper()
	guard := make(chan os.Signal, 16)
	signal.Notify(guard, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(guard) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.run()
	}()
	t.Cleanup(func() {
		_ = d.Stop(nil)
		<-done
	})
}

func watching(d *Daemon, paths ...string) bool {
	got := d.watchedPaths()
	if len(got) != len(paths) {
		return false
	}
	for _, p := range paths {
		if _, ok := d.handle(p); !ok {
			return false
		}
	}
	return true
}

func Test_HandEditedConfigReloadsOnSIGHUP(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)
	shortSocket(t)
	d.netInterval = time.Hour

	writeConfigByHand(t, `{"repos":["/r/a"],"envs":[]}`)
	startRun(t, d)
	waitUntil(t, func() bool { return watching(d, "/r/a") })

	writeConfigByHand(t, `{"repos":["/r/a/","/r/b","/r/b"],"envs":["X=1"]}`)
	hupUntil(t, func() bool { return watching(d, "/r/a", "/r/b") })
	assert.DeepEqual(t, d.currentEnvs(), []string{"X=1"})
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.starts) == 3 })
	waitUntil(t, func() bool { return atomic.LoadInt32(&f.running) == 2 })

	logs := captureLog(t)
	writeConfigByHand(t, `{"repos":["/r/a",`)
	hupUntil(t, func() bool { return strings.Contains(logs.String(), "failed to reload config") })
	assert.Assert(t, watching(d, "/r/a", "/r/b"))

	writeConfigByHand(t, `{"repos":["/r/b"]}`)
	hupUntil(t, func() bool { return watching(d, "/r/b") })
	waitUntil(t, func() bool {
		_, ok := d.state.Get("/r/a")
		return !ok
	})
}

func Test_BrokenConfigAtStartRecoversOnSIGHUP(t *testing.T) {
	f := &fakeWatchers{}
	d := newTestDaemon(t, f)
	shortSocket(t)
	d.netInterval = time.Hour
	d.configRetryDelay = 5 * time.Millisecond

	file := common.StateFilePath()
	assert.NilError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	src := common.NewRepoStateStore()
	src.Ensure("/r/a")
	src.SetPaused("/r/a", true)
	_, err := src.SavePersisted(file, nil)
	assert.NilError(t, err)
	before, err := os.ReadFile(file)
	assert.NilError(t, err)

	logs := captureLog(t)
	writeConfigByHand(t, `{"repos":[`)
	startRun(t, d)
	waitUntil(t, func() bool { return strings.Contains(logs.String(), "no repos are monitored") })
	assert.Assert(t, d.ctx.Err() == nil)
	assert.Equal(t, len(d.watchedPaths()), 0)

	time.Sleep(50 * time.Millisecond)
	after, err := os.ReadFile(file)
	assert.NilError(t, err)
	assert.DeepEqual(t, before, after)

	writeConfigByHand(t, `{"repos":["/r/a","/r/b"]}`)
	hupUntil(t, func() bool { return watching(d, "/r/a", "/r/b") })
	h, _ := d.handle("/r/a")
	assert.Assert(t, h.paused.Load())
	st, _ := d.state.Get("/r/a")
	assert.Equal(t, st.Status, common.StatusPaused)
}
