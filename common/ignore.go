package common

import (
	"context"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ztrue/tracerr"
)

const (
	ignoreCacheTTL        = time.Minute
	ignoreCacheMaxEntries = 4096
)

type ignoreEntry struct {
	ignored bool
	at      time.Time
}

type repoIgnoreCache struct {
	resolvedRoot string
	entries      map[string]ignoreEntry
}

var (
	ignoreCacheMu sync.Mutex
	ignoreCaches  = map[string]*repoIgnoreCache{}
)

func ShouldIgnoreFile(repoPath string, filePath string) (bool, error) {
	rel, ok := relativeToRepo(repoPath, filePath)
	if !ok {
		return true, nil
	}
	if rel == "." {
		return false, nil
	}
	if isGitInternalPath(rel) {
		return true, nil
	}

	base := path.Base(rel)
	if base == ".gitignore" {
		invalidateIgnoreCache(repoPath)
	}
	if isTempFileName(base) {
		return true, nil
	}

	return isFileIgnoredByGit(repoPath, rel)
}

func isTempFileName(name string) bool {
	return strings.HasSuffix(name, ".swp") ||
		strings.HasPrefix(name, "~") ||
		strings.HasSuffix(name, "~")
}

func isGitInternalPath(rel string) bool {
	return rel == ".git" ||
		strings.HasPrefix(rel, ".git/") ||
		strings.HasSuffix(rel, "/.git") ||
		strings.Contains(rel, "/.git/")
}

func relativeToRepo(repoPath, filePath string) (string, bool) {
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(repoPath, filePath)
	}
	if rel, ok := relWithin(repoPath, filePath); ok {
		return rel, true
	}
	resolved := resolvedRepoRoot(repoPath)
	if resolved != repoPath {
		if rel, ok := relWithin(resolved, filePath); ok {
			return rel, true
		}
	}
	resolvedFile := ResolvePath(filePath)
	if resolvedFile == filePath {
		return "", false
	}
	if rel, ok := relWithin(resolved, resolvedFile); ok {
		return rel, true
	}
	return relWithin(repoPath, resolvedFile)
}

func relWithin(root, filePath string) (string, bool) {
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

func repoCache(repoPath string) *repoIgnoreCache {
	c, ok := ignoreCaches[repoPath]
	if !ok {
		c = &repoIgnoreCache{entries: map[string]ignoreEntry{}}
		ignoreCaches[repoPath] = c
	}
	return c
}

func resolvedRepoRoot(repoPath string) string {
	ignoreCacheMu.Lock()
	c := repoCache(repoPath)
	resolved := c.resolvedRoot
	ignoreCacheMu.Unlock()
	if resolved != "" {
		return resolved
	}

	resolved = repoPath
	if r, err := filepath.EvalSymlinks(repoPath); err == nil {
		resolved = r
	}
	ignoreCacheMu.Lock()
	repoCache(repoPath).resolvedRoot = resolved
	ignoreCacheMu.Unlock()
	return resolved
}

func invalidateIgnoreCache(repoPath string) {
	ignoreCacheMu.Lock()
	repoCache(repoPath).entries = map[string]ignoreEntry{}
	ignoreCacheMu.Unlock()
}

func isFileIgnoredByGit(repoPath string, filePath string) (bool, error) {
	rel, ok := relativeToRepo(repoPath, filePath)
	if !ok {
		return true, nil
	}

	var dirs []string
	for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
		dirs = append(dirs, d+"/")
	}

	now := time.Now()
	var query []string
	ignoreCacheMu.Lock()
	c := repoCache(repoPath)
	for i := len(dirs) - 1; i >= 0; i-- {
		e, hit := c.entries[dirs[i]]
		if hit && now.Sub(e.at) < ignoreCacheTTL {
			if e.ignored {
				ignoreCacheMu.Unlock()
				return true, nil
			}
			continue
		}
		query = append(query, dirs[i])
	}
	if e, hit := c.entries[rel]; hit && now.Sub(e.at) < ignoreCacheTTL {
		ignoreCacheMu.Unlock()
		return e.ignored, nil
	}
	ignoreCacheMu.Unlock()
	query = append(query, rel)

	ignored, err := checkIgnore(repoPath, query)
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	ignoreCacheMu.Lock()
	c = repoCache(repoPath)
	if len(c.entries)+len(query) > ignoreCacheMaxEntries {
		c.entries = map[string]ignoreEntry{}
	}
	result := false
	for _, q := range query {
		c.entries[q] = ignoreEntry{ignored: ignored[q], at: now}
		if ignored[q] {
			result = true
		}
	}
	ignoreCacheMu.Unlock()

	return result, nil
}

func checkIgnore(repoPath string, paths []string) (map[string]bool, error) {
	cfg := RepoConfig{RepoPath: repoPath}
	stdin := []byte(strings.Join(paths, "\x00") + "\x00")
	out, err := runGit(context.Background(), cfg, nil, []string{"check-ignore", "-z", "--stdin"}, stdin)
	result := map[string]bool{}
	if err != nil {
		if gitExitCode(err) == 1 {
			return result, nil
		}
		return nil, err
	}
	for _, p := range strings.Split(out.String(), "\x00") {
		if p != "" {
			result[p] = true
		}
	}
	return result, nil
}
