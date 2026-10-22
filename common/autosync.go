package common

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gen2brain/beeep"
	"github.com/ztrue/tracerr"
)

type syncRun struct {
	ctx       context.Context
	cfg       RepoConfig
	prev      RepoState
	info      repoInfo
	committed bool
	pulled    int
	pushed    int
	dropped   int
	ahead     int
	behind    int
	counted   bool
	guarded   bool
	forced    bool
}

func AutoSync(repoConfig RepoConfig) error {
	return AutoSyncCtx(context.Background(), repoConfig)
}

func AutoSyncCtx(ctx context.Context, repoConfig RepoConfig) error {
	var prev RepoState
	if repoConfig.State != nil {
		repoConfig.State.Ensure(repoConfig.RepoPath)
		prev, _ = repoConfig.State.Get(repoConfig.RepoPath)
	}

	s := &syncRun{ctx: ctx, cfg: repoConfig, prev: prev}
	stage, err := s.prepare()
	if repoConfig.LocalOnly {
		return s.runLocal(err)
	}
	if err != nil {
		if ctx.Err() != nil {
			return tracerr.Wrap(err)
		}
		if code := preflightCode(err); code != "" {
			s.markPreflight(err.Error(), code)
			return tracerr.Wrap(err)
		}
		return s.fail(stage, err)
	}

	ensureRemoteFacts(ctx, repoConfig, s.info.CommonDir)

	if repoConfig.State != nil {
		repoConfig.State.MarkSyncing(repoConfig.RepoPath)
	}

	stage, err = s.run()
	if err != nil {
		if ctx.Err() != nil {
			s.restoreStatus()
			return tracerr.Wrap(err)
		}
		return s.fail(stage, err)
	}
	if s.guarded {
		return s.stayInConflict()
	}
	return s.succeed()
}

func (s *syncRun) prepare() (string, error) {
	info, err := preflight(s.ctx, s.cfg)
	s.info = info
	if preflightCode(err) != preflightNoHead || !upstreamConfigured(s.ctx, s.cfg, info.Branch) {
		return "preflight", err
	}
	if cerr := s.commit(); cerr != nil {
		return "commit", cerr
	}
	if !s.committed {
		return "preflight", err
	}
	info, err = preflight(s.ctx, s.cfg)
	s.info = info
	return "preflight", err
}

func (s *syncRun) runLocal(err error) error {
	if err == nil {
		err = s.commit()
	}
	if s.ctx.Err() == nil {
		s.finish()
	}
	if err != nil {
		return tracerr.Wrap(err)
	}
	return nil
}

func (s *syncRun) fetch() error {
	forced, err := fetch(s.ctx, s.cfg, s.info)
	s.forced = s.forced || forced
	return err
}

func (s *syncRun) run() (string, error) {
	if err := s.commit(); err != nil {
		return "commit", err
	}
	if err := s.fetch(); err != nil {
		return "fetch", err
	}
	if s.conflictUnchanged() {
		s.guarded = true
		return "", nil
	}
	if err := s.rebase(); err != nil {
		return "rebase", err
	}
	if s.ahead > 0 {
		if err := s.push(); err != nil {
			return "push", err
		}
		s.pushed = s.ahead
		s.ahead = 0
	}
	return "", nil
}

func (s *syncRun) commit() error {
	committed, err := commit(s.ctx, s.cfg)
	if committed {
		s.committed = true
	}
	return err
}

func (s *syncRun) count() error {
	ahead, behind, err := aheadBehind(s.ctx, s.cfg, s.info)
	if err != nil {
		s.counted = false
		return err
	}
	s.ahead, s.behind, s.counted = ahead, behind, true
	return nil
}

func (s *syncRun) rebase() error {
	if err := s.count(); err != nil {
		return err
	}
	if s.behind == 0 && !s.forced {
		return nil
	}
	ahead, behind, forkPoint := s.ahead, s.behind, s.forced

	err := rebase(s.ctx, s.cfg, s.info, forkPoint)
	if errors.Is(err, errDirtyWorktree) {
		if err := s.commit(); err != nil {
			return err
		}
		err = rebase(s.ctx, s.cfg, s.info, forkPoint)
	}
	if err != nil {
		return err
	}
	s.forced = false
	s.pulled += behind
	if err := s.count(); err != nil {
		return err
	}
	if forkPoint && s.ahead < ahead {
		s.dropped += ahead - s.ahead
	}
	return nil
}

func (s *syncRun) conflictUnchanged() bool {
	p := s.prev
	if s.cfg.Force || !p.Conflict || p.ConflictHead == "" || p.ConflictUp == "" {
		return false
	}
	up, err := revParse(s.ctx, s.cfg, s.info.Upstream)
	if err != nil || up != p.ConflictUp {
		return false
	}
	if !isAncestor(s.ctx, s.cfg, p.ConflictHead, "HEAD") {
		return false
	}
	return !isAncestor(s.ctx, s.cfg, up, "HEAD")
}

func (s *syncRun) changed() bool {
	return s.committed || s.pulled > 0 || s.pushed > 0 || s.dropped > 0
}

func (s *syncRun) activity() string {
	var parts []string
	if s.committed {
		parts = append(parts, "committed")
	}
	if s.pulled > 0 {
		parts = append(parts, fmt.Sprintf("pulled %d", s.pulled))
	}
	if s.dropped > 0 {
		parts = append(parts, fmt.Sprintf("dropped %d rewritten upstream", s.dropped))
	}
	if s.pushed > 0 {
		parts = append(parts, fmt.Sprintf("pushed %d", s.pushed))
	}
	if len(parts) == 0 {
		return "up to date"
	}
	return strings.Join(parts, ", ")
}

func (s *syncRun) succeed() error {
	path := s.cfg.RepoPath
	activity := s.activity()
	if s.cfg.State != nil {
		s.cfg.State.MarkSuccess(path, activity)
	}

	upstream := s.info.UpstreamShort
	if s.pulled > 0 {
		emitEvent(s.cfg, NewEvent(EventPull, path, fmt.Sprintf("pulled %d commit(s) from %s", s.pulled, upstream), ""))
	}
	if s.dropped > 0 {
		emitEvent(s.cfg, NewEvent(EventPull, path, fmt.Sprintf("%s was force-pushed; dropped %d local commit(s) that were removed upstream", upstream, s.dropped), ""))
	}
	if s.pushed > 0 {
		emitEvent(s.cfg, NewEvent(EventPush, path, fmt.Sprintf("pushed %d commit(s) to %s", s.pushed, upstream), ""))
	}
	if isUnhealthy(s.prev.Status) {
		emitEvent(s.cfg, NewEvent(EventRecovered, path, "recovered from "+s.prev.Status, StatusOK))
	}
	if s.changed() {
		emitEvent(s.cfg, NewEvent(EventSynced, path, activity, StatusOK))
	}

	s.finish()
	return nil
}

func isUnhealthy(status string) bool {
	switch status {
	case StatusError, StatusOffline, StatusConflict, StatusPreflight:
		return true
	}
	return false
}

func (s *syncRun) stayInConflict() error {
	if s.cfg.State != nil {
		msg := s.prev.LastError
		if msg == "" {
			msg = "rebase conflict"
		}
		s.cfg.State.MarkConflict(s.cfg.RepoPath, s.prev.ConflictFiles, msg)
	}
	s.finish()
	return nil
}

func (s *syncRun) fail(stage string, err error) error {
	path := s.cfg.RepoPath

	var conflict *rebaseConflictError
	if errors.As(err, &conflict) {
		s.handleConflict(conflict)
		s.finish()
		return tracerr.Wrap(err)
	}
	if code := preflightCode(err); code != "" {
		s.markPreflight(err.Error(), code)
		s.finish()
		return tracerr.Wrap(err)
	}

	code := ClassifyError(err)
	msg := errorMessage(stage, err)

	switch {
	case code == CodeNetwork || (code == CodeTimeout && isNetworkTimeout(err)):
		if s.cfg.State != nil {
			s.cfg.State.MarkOffline(path, msg, 0)
			if code != CodeNetwork {
				s.cfg.State.Update(path, func(st *RepoState) { st.ErrorCode = code })
			}
		}
		if s.prev.Status != StatusOffline {
			emitEvent(s.cfg, NewCodedEvent(EventOffline, path, msg, code))
		}
	default:
		if s.cfg.State != nil {
			s.cfg.State.MarkErrorCode(path, code, msg)
		}
		if s.prev.Status != StatusError || s.prev.ErrorCode != code || s.prev.LastError != msg {
			emitEvent(s.cfg, NewCodedEvent(EventError, path, msg, code))
		}
	}

	s.finish()
	return tracerr.Wrap(err)
}

func (s *syncRun) handleConflict(c *rebaseConflictError) {
	path := s.cfg.RepoPath
	isNew := !s.prev.Conflict || !equalStrings(s.prev.ConflictFiles, c.Files) || s.cfg.Force

	if s.cfg.State != nil {
		s.cfg.State.SetConflictRefs(path, c.Head, c.Upstream)
		s.cfg.State.MarkConflict(path, c.Files, "rebase conflict")
	}
	if isNew {
		logConflict(s.info.GitDir, c.Files)
		emitConflict(s.cfg, c.Files)
	}
	if !s.prev.Conflict {
		notifyIfNoClients(s.cfg, "Git Auto Sync - Conflict", "Could not rebase for - "+path)
	}
}

func (s *syncRun) markPreflight(msg, code string) {
	path := s.cfg.RepoPath
	if s.prev.Status != StatusPreflight || s.prev.Preflight != msg {
		emitEvent(s.cfg, NewCodedEvent(EventPreflight, path, msg, code))
	}
	if s.cfg.State != nil {
		s.cfg.State.MarkPreflight(path, msg)
		s.cfg.State.Update(path, func(st *RepoState) { st.ErrorCode = code })
	}
}

func (s *syncRun) restoreStatus() {
	if s.cfg.State == nil {
		return
	}
	prevStatus := s.prev.Status
	s.cfg.State.Update(s.cfg.RepoPath, func(st *RepoState) {
		if st.Status == StatusSyncing {
			st.Status = prevStatus
		}
	})
}

func (s *syncRun) finish() {
	if s.cfg.State == nil {
		return
	}
	path := s.cfg.RepoPath
	s.cfg.State.MarkChecked(path, s.changed())
	if s.info.Branch == "" {
		return
	}
	if !s.counted || s.guarded {
		if err := s.count(); err != nil {
			s.ahead, s.behind = 0, 0
		}
	}
	s.cfg.State.SetBranchInfo(path, s.info.Branch, s.info.UpstreamShort, s.ahead, s.behind)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func emitEvent(cfg RepoConfig, ev Event) {
	if cfg.Emitter != nil {
		cfg.Emitter.Emit(ev)
	}
}

func emitConflict(cfg RepoConfig, files []string) {
	msg := "rebase conflict"
	if len(files) > 0 {
		msg = "rebase conflict: " + strings.Join(files, ", ")
	}
	emitEvent(cfg, NewCodedEvent(EventConflict, cfg.RepoPath, msg, CodeConflict))
}

func notifyIfNoClients(cfg RepoConfig, title, message string) {
	if cfg.Clients != nil && cfg.Clients.HasClients() {
		return
	}
	_ = beeep.Alert(title, message, "")
}
