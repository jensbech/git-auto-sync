package common

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

const persistedStateVersion = 1

type persistedRepoState struct {
	Path          string   `json:"path"`
	Paused        bool     `json:"paused,omitempty"`
	LastSyncTs    int64    `json:"lastSyncTs,omitempty"`
	LastChangeTs  int64    `json:"lastChangeTs,omitempty"`
	LastCheckTs   int64    `json:"lastCheckTs,omitempty"`
	Conflict      bool     `json:"conflict,omitempty"`
	ConflictFiles []string `json:"conflictFiles,omitempty"`
	ConflictHead  string   `json:"conflictHead,omitempty"`
	ConflictUp    string   `json:"conflictUpstream,omitempty"`
}

type persistedStateFile struct {
	Version int                  `json:"version"`
	Repos   []persistedRepoState `json:"repos"`
}

func StateFilePath() string {
	return filepath.Join(StateDir(), "state.json")
}

func (s *RepoStateStore) MarshalPersisted() ([]byte, error) {
	s.mu.RLock()
	repos := make([]persistedRepoState, 0, len(s.states))
	for _, st := range s.states {
		p := persistedRepoState{
			Path:         st.Path,
			Paused:       st.Paused,
			LastSyncTs:   st.LastSyncTs,
			LastChangeTs: st.LastChangeTs,
			LastCheckTs:  st.LastCheckTs,
			ConflictHead: st.ConflictHead,
			ConflictUp:   st.ConflictUp,
		}
		if st.Conflict {
			p.Conflict = true
			p.ConflictFiles = append([]string(nil), st.ConflictFiles...)
		}
		repos = append(repos, p)
	}
	s.mu.RUnlock()

	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	data, err := json.MarshalIndent(persistedStateFile{Version: persistedStateVersion, Repos: repos}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (s *RepoStateStore) SavePersisted(file string, previous []byte) ([]byte, error) {
	data, err := s.MarshalPersisted()
	if err != nil {
		return previous, err
	}
	if previous != nil && bytes.Equal(data, previous) {
		return previous, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return previous, err
	}
	if err := WriteFileAtomic(file, data, 0o600); err != nil {
		return previous, err
	}
	return data, nil
}

func (s *RepoStateStore) LoadPersisted(file string, keep func(string) bool) (int, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var pf persistedStateFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range pf.Repos {
		if p.Path == "" || (keep != nil && !keep(p.Path)) {
			continue
		}
		st, ok := s.states[p.Path]
		if !ok {
			st = &RepoState{Path: p.Path, Status: StatusIdle}
			s.states[p.Path] = st
		}
		st.Paused = p.Paused
		st.LastSyncTs = p.LastSyncTs
		st.LastChangeTs = p.LastChangeTs
		st.LastCheckTs = p.LastCheckTs
		st.ConflictHead = p.ConflictHead
		st.ConflictUp = p.ConflictUp
		if p.Conflict {
			st.Conflict = true
			st.ConflictFiles = p.ConflictFiles
			st.Status = StatusConflict
			st.ErrorCode = CodeConflict
			st.LastError = "rebase conflict"
		}
		if p.Paused {
			st.Status = StatusPaused
		}
		n++
	}
	return n, nil
}
