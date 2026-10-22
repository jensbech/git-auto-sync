package common

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

func Test_RepoStatePersistRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "state.json")

	s := NewRepoStateStore()
	s.Ensure("/r/a")
	s.Ensure("/r/b")
	s.Ensure("/r/gone")
	s.SetPaused("/r/a", true)
	s.MarkSuccess("/r/a", "synced")
	s.MarkChecked("/r/a", true)
	s.MarkConflict("/r/b", []string{"x.md"}, "rebase conflict")
	s.SetConflictRefs("/r/b", "h1", "u1")

	data, err := s.SavePersisted(file, nil)
	assert.NilError(t, err)
	same, err := s.SavePersisted(file, data)
	assert.NilError(t, err)
	assert.DeepEqual(t, same, data)

	info, err := os.Stat(file)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))

	r := NewRepoStateStore()
	r.Ensure("/r/a")
	r.Ensure("/r/b")
	n, err := r.LoadPersisted(file, func(p string) bool { return p != "/r/gone" })
	assert.NilError(t, err)
	assert.Equal(t, n, 2)

	a, _ := r.Get("/r/a")
	orig, _ := s.Get("/r/a")
	assert.Equal(t, a.Paused, true)
	assert.Equal(t, a.Status, StatusPaused)
	assert.Equal(t, a.LastSyncTs, orig.LastSyncTs)
	assert.Equal(t, a.LastChangeTs, orig.LastChangeTs)

	b, _ := r.Get("/r/b")
	assert.Equal(t, b.Status, StatusConflict)
	assert.Equal(t, b.Conflict, true)
	assert.DeepEqual(t, b.ConflictFiles, []string{"x.md"})
	assert.Equal(t, b.ConflictHead, "h1")
	assert.Equal(t, b.ConflictUp, "u1")

	_, ok := r.Get("/r/gone")
	assert.Assert(t, !ok)
}

func Test_RepoStateLoadMissingFile(t *testing.T) {
	s := NewRepoStateStore()
	n, err := s.LoadPersisted(filepath.Join(t.TempDir(), "nope.json"), nil)
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func Test_RepoStateListenersSeeUpdatesInOrder(t *testing.T) {
	s := NewRepoStateStore()
	s.Ensure("/r/a")

	var mu sync.Mutex
	var seqs []uint64
	var last RepoState
	s.OnChange(func(st RepoState) {
		cur, _ := s.Get(st.Path)
		mu.Lock()
		defer mu.Unlock()
		seqs = append(seqs, st.Seq)
		last = st
		if cur.Seq != st.Seq {
			t.Errorf("store seq %d differs from delivered seq %d", cur.Seq, st.Seq)
		}
	})

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch i % 3 {
				case 0:
					s.MarkSyncing("/r/a")
				case 1:
					s.MarkOffline("/r/a", "offline", int64(i))
				default:
					s.SetBranchInfo("/r/a", "main", "origin/main", g, i)
				}
			}
		}(g)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, len(seqs), 8*200)
	for i := 1; i < len(seqs); i++ {
		assert.Assert(t, seqs[i] > seqs[i-1], "seq %d after %d", seqs[i], seqs[i-1])
	}
	final, _ := s.Get("/r/a")
	assert.DeepEqual(t, last, final)
}

func Test_SetWatchingDoesNotResurrectRemovedRepo(t *testing.T) {
	s := NewRepoStateStore()
	s.Ensure("/r/a")
	s.Remove("/r/a")
	s.SetWatching("/r/a", false)
	_, ok := s.Get("/r/a")
	assert.Assert(t, !ok)
}
