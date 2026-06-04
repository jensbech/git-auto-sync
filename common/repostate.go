package common

import (
	"sync"
	"time"
)

const (
	StatusIdle      = "idle"
	StatusSyncing   = "syncing"
	StatusOK        = "ok"
	StatusError     = "error"
	StatusConflict  = "conflict"
	StatusPreflight = "preflight"
	StatusPaused    = "paused"
)

type RepoState struct {
	Path          string   `json:"path"`
	Status        string   `json:"status"`
	Branch        string   `json:"branch,omitempty"`
	Upstream      string   `json:"upstream,omitempty"`
	Ahead         int      `json:"ahead"`
	Behind        int      `json:"behind"`
	LastSyncTs    int64    `json:"lastSyncTs,omitempty"`
	LastActivity  string   `json:"lastActivity,omitempty"`
	LastError     string   `json:"lastError,omitempty"`
	Preflight     string   `json:"preflight,omitempty"`
	Paused        bool     `json:"paused"`
	Conflict      bool     `json:"conflict"`
	ConflictFiles []string `json:"conflictFiles,omitempty"`
	Watching      bool     `json:"watching"`
	BatchPending  bool     `json:"batchPending,omitempty"`
	BatchDueTs    int64    `json:"batchDueTs,omitempty"`
	PollInterval  int      `json:"pollInterval,omitempty"`
	BatchWindow   int      `json:"batchWindow,omitempty"`
}

type RepoStateStore struct {
	mu        sync.RWMutex
	states    map[string]*RepoState
	listeners []func(RepoState)
}

func NewRepoStateStore() *RepoStateStore {
	return &RepoStateStore{states: make(map[string]*RepoState)}
}

func (s *RepoStateStore) OnChange(fn func(RepoState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

func (s *RepoStateStore) Ensure(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[path]; !ok {
		s.states[path] = &RepoState{Path: path, Status: StatusIdle}
	}
}

func (s *RepoStateStore) Remove(path string) {
	s.mu.Lock()
	delete(s.states, path)
	s.mu.Unlock()
}

func (s *RepoStateStore) Update(path string, fn func(*RepoState)) RepoState {
	s.mu.Lock()
	st, ok := s.states[path]
	if !ok {
		st = &RepoState{Path: path, Status: StatusIdle}
		s.states[path] = st
	}
	fn(st)
	snap := *st
	listeners := append([]func(RepoState){}, s.listeners...)
	s.mu.Unlock()

	for _, l := range listeners {
		l(snap)
	}
	return snap
}

func (s *RepoStateStore) Get(path string) (RepoState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[path]
	if !ok {
		return RepoState{}, false
	}
	return *st, true
}

func (s *RepoStateStore) Snapshot() []RepoState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoState, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, *st)
	}
	return out
}

func (s *RepoStateStore) MarkSyncing(path string) {
	s.Update(path, func(st *RepoState) {
		if st.Paused {
			return
		}
		st.Status = StatusSyncing
	})
}

func (s *RepoStateStore) MarkSuccess(path string, activity string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusOK
		st.LastActivity = activity
		st.LastSyncTs = time.Now().Unix()
		st.LastError = ""
		st.Preflight = ""
		st.Conflict = false
		st.ConflictFiles = nil
	})
}

func (s *RepoStateStore) MarkError(path string, msg string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusError
		st.LastError = msg
	})
}

func (s *RepoStateStore) MarkConflict(path string, files []string, msg string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusConflict
		st.Conflict = true
		st.ConflictFiles = files
		st.LastError = msg
	})
}

func (s *RepoStateStore) MarkPreflight(path string, reason string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusPreflight
		st.Preflight = reason
	})
}

func (s *RepoStateStore) SetWatching(path string, watching bool) {
	s.Update(path, func(st *RepoState) {
		st.Watching = watching
	})
}

func (s *RepoStateStore) SetPaused(path string, paused bool) {
	s.Update(path, func(st *RepoState) {
		st.Paused = paused
		if paused {
			st.Status = StatusPaused
		} else if st.Status == StatusPaused {
			st.Status = StatusIdle
		}
	})
}

func (s *RepoStateStore) SetBranchInfo(path, branch, upstream string, ahead, behind int) {
	s.Update(path, func(st *RepoState) {
		st.Branch = branch
		st.Upstream = upstream
		st.Ahead = ahead
		st.Behind = behind
	})
}

func (s *RepoStateStore) SetBatchPending(path string, pending bool, dueTs int64) {
	s.Update(path, func(st *RepoState) {
		st.BatchPending = pending
		st.BatchDueTs = dueTs
	})
}

func (s *RepoStateStore) SetSettings(path string, pollSec, batchSec int) {
	s.Update(path, func(st *RepoState) {
		st.PollInterval = pollSec
		st.BatchWindow = batchSec
	})
}
