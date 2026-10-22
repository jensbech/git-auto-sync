package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
)

const gitConfigTimeout = 30 * time.Second

var errNotWatching = common.NewCodedError(common.CodeNotWatching, "repo is not being watched")

func cleanAbs(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func (d *Daemon) handle(repoPath string) (*watcherHandle, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	h, ok := d.watchers[repoPath]
	return h, ok
}

func (d *Daemon) lookupWatcher(path string) (string, *watcherHandle, bool) {
	if path == "" {
		return "", nil, false
	}
	candidates := []string{path, cleanAbs(path), common.ResolvePath(path)}
	for _, c := range candidates {
		if h, ok := d.handle(c); ok {
			return c, h, true
		}
	}
	resolved := common.ResolvePath(path)
	for _, k := range d.watchedPaths() {
		if common.ResolvePath(k) == resolved {
			if h, ok := d.handle(k); ok {
				return k, h, true
			}
		}
	}
	if root, err := common.RepoRoot(path); err == nil {
		if h, ok := d.handle(root); ok {
			return root, h, true
		}
	}
	return "", nil, false
}

func (d *Daemon) currentEnvs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.envs...)
}

func (d *Daemon) AddRepo(path string) (string, error) {
	root, err := common.RepoRoot(path)
	if err != nil {
		if common.ErrorCode(err) == "" {
			err = common.NewCodedError(common.CodeNotARepo, "not a git repository: "+path)
		}
		return "", err
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()

	config, err := cfg.Read()
	if err != nil {
		return "", common.NewCodedError(common.CodeConfig, "cannot read config: "+err.Error())
	}
	for _, rp := range config.Repos {
		if rp == root || common.ResolvePath(rp) == root {
			d.reconcileWatchers(d.ctx, config)
			return rp, common.NewCodedError(common.CodeAlreadyExists, "already monitoring "+rp)
		}
	}
	config.Repos = append(config.Repos, root)
	if err := cfg.Write(config); err != nil {
		return "", common.NewCodedError(common.CodeConfig, "cannot write config: "+err.Error())
	}
	d.state.Ensure(root)
	d.reconcileWatchers(d.ctx, config)
	return root, nil
}

func matchConfiguredRepo(repos []string, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	candidates := []string{path, cleanAbs(path), common.ResolvePath(path)}
	for _, c := range candidates {
		for _, rp := range repos {
			if rp == c {
				return rp, true
			}
		}
	}
	resolved := common.ResolvePath(path)
	for _, rp := range repos {
		if common.ResolvePath(rp) == resolved {
			return rp, true
		}
	}
	if root, err := common.RepoRoot(path); err == nil {
		for _, rp := range repos {
			if rp == root || common.ResolvePath(rp) == root {
				return rp, true
			}
		}
	}
	return "", false
}

func (d *Daemon) RemoveRepo(path string) error {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()

	config, err := cfg.Read()
	if err != nil {
		return common.NewCodedError(common.CodeConfig, "cannot read config: "+err.Error())
	}
	target, found := matchConfiguredRepo(config.Repos, path)
	if !found {
		return common.NewCodedError(common.CodeNotFound, "not monitoring "+path)
	}
	targetResolved := common.ResolvePath(target)
	repos := make([]string, 0, len(config.Repos))
	for _, rp := range config.Repos {
		if rp != target && common.ResolvePath(rp) != targetResolved {
			repos = append(repos, rp)
		}
	}
	config.Repos = repos
	if err := cfg.Write(config); err != nil {
		return common.NewCodedError(common.CodeConfig, "cannot write config: "+err.Error())
	}
	d.reconcileWatchers(d.ctx, config)
	return nil
}

func repoMissingError(key string) error {
	if common.RepoDirMissing(key) {
		return common.NewCodedError(common.CodeRepoMissing, common.RepoMissingMessage(key))
	}
	return nil
}

func kick(h *watcherHandle) {
	select {
	case h.immediate <- struct{}{}:
	default:
	}
}

func (d *Daemon) SyncNow(path string, force bool) error {
	key, h, ok := d.lookupWatcher(path)
	if !ok {
		return errNotWatching
	}
	if h.paused.Load() {
		return common.NewCodedError(common.CodeBadRequest, "sync skipped: "+key+" is paused")
	}
	if err := repoMissingError(key); err != nil {
		return err
	}
	if force {
		h.force.Store(true)
	}
	kick(h)
	return nil
}

func (d *Daemon) watchedPaths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	paths := make([]string, 0, len(d.watchers))
	for p := range d.watchers {
		paths = append(paths, p)
	}
	return paths
}

func (d *Daemon) SyncAll() {
	for _, p := range d.watchedPaths() {
		_ = d.SyncNow(p, false)
	}
}

func (d *Daemon) SetPaused(path string, paused bool) error {
	key, h, ok := d.lookupWatcher(path)
	if !ok {
		return errNotWatching
	}
	was := h.paused.Swap(paused)
	d.state.SetPaused(key, paused)
	if was == paused {
		return nil
	}
	if paused {
		d.emit(common.NewEvent(common.EventPaused, key, "", common.StatusPaused))
	} else {
		d.emit(common.NewEvent(common.EventResumed, key, "", ""))
		kick(h)
	}
	return nil
}

func (d *Daemon) emit(ev common.Event) {
	if d.emitter != nil {
		d.emitter.Emit(ev)
	}
}

func (d *Daemon) SetAllPaused(paused bool) {
	for _, p := range d.watchedPaths() {
		_ = d.SetPaused(p, paused)
	}
}

func rebaseInProgress(repoCfg common.RepoConfig) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		out, err := common.GitCommand(repoCfg, []string{"rev-parse", "--git-path", name})
		if err != nil {
			return false, err
		}
		p := strings.TrimSpace(out.String())
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoCfg.RepoPath, p)
		}
		if _, err := os.Stat(p); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func (d *Daemon) AbortRebase(path string) error {
	key, h, ok := d.lookupWatcher(path)
	if !ok {
		return errNotWatching
	}
	if err := repoMissingError(key); err != nil {
		return err
	}

	repoCfg, err := common.NewRepoConfig(key)
	if err != nil {
		return common.NewCodedError(common.CodeConfig, "cannot load repo config: "+err.Error())
	}
	repoCfg.Env = append(repoCfg.Env, d.currentEnvs()...)

	lock := d.syncLock(key)
	lock.Lock()
	defer lock.Unlock()

	rebasing, err := rebaseInProgress(repoCfg)
	if err != nil {
		return common.NewCodedError(common.CodeUnknown, "cannot inspect repository: "+common.ErrorMessage(err))
	}
	if !rebasing {
		return common.NewCodedError(common.CodeBadRequest, "no rebase in progress in "+key)
	}

	if _, err := common.GitCommand(repoCfg, []string{"rebase", "--abort"}); err != nil {
		return common.NewCodedError(common.CodeUnknown, "git rebase --abort failed: "+common.ErrorMessage(err))
	}

	if err := common.Preflight(repoCfg); err != nil {
		code := common.CodeUnknown
		var pe *common.PreflightError
		if errors.As(err, &pe) && pe.Code != "" {
			code = pe.Code
		}
		d.state.MarkPreflightCode(key, code, common.ErrorMessage(err))
		return nil
	}
	d.state.ClearPreflight(key)
	if !h.paused.Load() {
		kick(h)
	}
	return nil
}

func runGitConfig(repoPath string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitConfigTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"config"}, args...)...)
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &gitConfigError{err: err, msg: msg}
	}
	return stdout.String(), nil
}

type gitConfigError struct {
	err error
	msg string
}

func (e *gitConfigError) Error() string { return e.msg }

func (e *gitConfigError) exitCode() int {
	var exitErr *exec.ExitError
	if errors.As(e.err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (d *Daemon) readRepoSettings(repoPath string) (map[string]interface{}, error) {
	key := repoPath
	if k, _, ok := d.lookupWatcher(repoPath); ok {
		key = k
	}
	if common.RepoDirMissing(key) {
		return nil, common.NewCodedError(common.CodeRepoMissing, common.RepoMissingMessage(key))
	}

	raw, err := common.ReadAutoSyncGitConfig(key)
	if err != nil {
		return nil, common.NewCodedError(common.CodeConfig, "cannot read settings: "+err.Error())
	}

	out := map[string]interface{}{
		"repo": key,
	}
	for _, name := range common.SettingNames() {
		gitKey, _ := common.SettingGitKey(name)
		val := strings.TrimSpace(raw[strings.ToLower(gitKey)])
		switch {
		case val == "":
			out[name] = nil
		case name == common.SettingPollInterval || name == common.SettingBatchWindow:
			if n, err := strconv.Atoi(val); err == nil {
				out[name] = n
			} else {
				out[name] = val
			}
		default:
			out[name] = val
		}
	}
	return out, nil
}

func (d *Daemon) writeRepoSettings(repoPath string, settings map[string]string) error {
	key, _, ok := d.lookupWatcher(repoPath)
	if !ok {
		return errNotWatching
	}
	if len(settings) == 0 {
		return common.NewCodedError(common.CodeBadRequest, "no settings given")
	}
	if err := repoMissingError(key); err != nil {
		return err
	}

	validated := make(map[string]string, len(settings))
	for name, value := range settings {
		v, err := common.ValidateSetting(name, value)
		if err != nil {
			return err
		}
		validated[name] = v
	}

	for _, name := range common.SettingNames() {
		value, ok := validated[name]
		if !ok {
			continue
		}
		gitKey, _ := common.SettingGitKey(name)
		fullKey := "auto-sync." + gitKey
		if value == "" {
			if _, err := runGitConfig(key, "--unset-all", fullKey); err != nil {
				var gce *gitConfigError
				if errors.As(err, &gce) && gce.exitCode() == 5 {
					continue
				}
				return common.NewCodedError(common.CodeConfig, fmt.Sprintf("cannot unset %s: %v", fullKey, err))
			}
			continue
		}
		if _, err := runGitConfig(key, "--replace-all", fullKey, value); err != nil {
			return common.NewCodedError(common.CodeConfig, fmt.Sprintf("cannot set %s: %v", fullKey, err))
		}
	}
	return nil
}
