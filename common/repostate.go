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
	StatusOffline   = "offline"
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
	ErrorCode     string   `json:"errorCode,omitempty"`
	LastChangeTs  int64    `json:"lastChangeTs,omitempty"`
	LastCheckTs   int64    `json:"lastCheckTs,omitempty"`
	NextRetryTs   int64    `json:"nextRetryTs,omitempty"`
	Failures      int      `json:"failures"`
	ConflictHead  string   `json:"conflictHead,omitempty"`
	ConflictUp    string   `json:"conflictUpstream,omitempty"`
	Seq           uint64   `json:"seq"`
}

type RepoStateStore struct {
	notifyMu  sync.Mutex
	mu        sync.RWMutex
	states    map[string]*RepoState
	listeners []func(RepoState)
	seq       uint64
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
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.mu.Lock()
	st, ok := s.states[path]
	if !ok {
		st = &RepoState{Path: path, Status: StatusIdle}
		s.states[path] = st
	}
	fn(st)
	s.seq++
	st.Seq = s.seq
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
		st.ErrorCode = ""
		st.Preflight = ""
		st.Conflict = false
		st.ConflictFiles = nil
		st.ConflictHead = ""
		st.ConflictUp = ""
		st.NextRetryTs = 0
		st.Failures = 0
	})
}

func (s *RepoStateStore) MarkError(path string, msg string) {
	s.MarkErrorCode(path, CodeUnknown, msg)
}

func (s *RepoStateStore) MarkErrorCode(path, code, msg string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusError
		st.LastError = msg
		st.ErrorCode = code
		st.Preflight = ""
		st.Failures++
	})
}

func (s *RepoStateStore) MarkOffline(path, msg string, nextRetry int64) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusOffline
		st.LastError = msg
		st.ErrorCode = CodeNetwork
		st.Preflight = ""
		st.NextRetryTs = nextRetry
		st.Failures++
	})
}

func (s *RepoStateStore) MarkChecked(path string, changed bool) {
	s.Update(path, func(st *RepoState) {
		now := time.Now().Unix()
		st.LastCheckTs = now
		if changed {
			st.LastChangeTs = now
		}
	})
}

func (s *RepoStateStore) SetConflictRefs(path, head, upstream string) {
	s.Update(path, func(st *RepoState) {
		st.ConflictHead = head
		st.ConflictUp = upstream
	})
}

func (s *RepoStateStore) MarkConflict(path string, files []string, msg string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusConflict
		st.Conflict = true
		st.ConflictFiles = files
		st.LastError = msg
		st.ErrorCode = CodeConflict
		st.Preflight = ""
	})
}

func (s *RepoStateStore) MarkPreflight(path string, reason string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusPreflight
		st.Preflight = reason
	})
}

func (s *RepoStateStore) MarkRepoMissing(path, msg string) bool {
	changed := false
	s.Update(path, func(st *RepoState) {
		changed = st.Status != StatusPreflight || st.ErrorCode != CodeRepoMissing || st.Preflight != msg
		st.Status = StatusPreflight
		st.Preflight = msg
		st.LastError = msg
		st.ErrorCode = CodeRepoMissing
		st.NextRetryTs = 0
		st.BatchPending = false
		st.BatchDueTs = 0
	})
	return changed
}

func (s *RepoStateStore) ClearRepoMissing(path string) bool {
	cleared := false
	s.UpdateExisting(path, func(st *RepoState) {
		if st.ErrorCode != CodeRepoMissing {
			return
		}
		cleared = true
		st.Preflight = ""
		st.LastError = ""
		st.ErrorCode = ""
		if st.Paused {
			st.Status = StatusPaused
		} else {
			st.Status = StatusIdle
		}
	})
	return cleared
}

func (s *RepoStateStore) MarkPreflightCode(path, code, reason string) {
	s.Update(path, func(st *RepoState) {
		st.Status = StatusPreflight
		st.Preflight = reason
		st.ErrorCode = code
	})
}

func (s *RepoStateStore) SetWatching(path string, watching bool) {
	s.UpdateExisting(path, func(st *RepoState) {
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

func (s *RepoStateStore) SetNextRetry(path string, ts int64) {
	s.Update(path, func(st *RepoState) {
		st.NextRetryTs = ts
	})
}

func (s *RepoStateStore) ClearPreflight(path string) {
	s.Update(path, func(st *RepoState) {
		st.Preflight = ""
		if st.Status == StatusPreflight {
			st.ErrorCode = ""
			if st.Paused {
				st.Status = StatusPaused
			} else {
				st.Status = StatusIdle
			}
		}
	})
}

func (s *RepoStateStore) UpdateExisting(path string, fn func(*RepoState)) bool {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.mu.Lock()
	st, ok := s.states[path]
	if !ok {
		s.mu.Unlock()
		return false
	}
	fn(st)
	s.seq++
	st.Seq = s.seq
	snap := *st
	listeners := append([]func(RepoState){}, s.listeners...)
	s.mu.Unlock()

	for _, l := range listeners {
		l(snap)
	}
	return true
}
