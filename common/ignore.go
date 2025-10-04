package common

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ztrue/tracerr"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/format/gitignore"
)

func ShouldIgnoreFile(repoPath string, filePath string) (bool, error) {
	if !filepath.IsAbs(filePath) {
		filePath = path.Join(repoPath, filePath)
	}

	fileName := filepath.Base(filePath)
	var isTempFile = strings.HasSuffix(fileName, ".swp") || // vim
		strings.HasPrefix(fileName, "~") || // emacs
		strings.HasSuffix(fileName, "~") // kate

	if isTempFile {
		return true, nil
	}

	relativePath := filePath[len(repoPath)+1:]
	if strings.HasPrefix(relativePath, ".git/") {
		return true, nil
	}

	_, err := isEmptyFile(filePath) // We still check existence errors but allow empty files
	if err != nil {
		return false, tracerr.Wrap(err)
	}
	// Previously empty files were ignored to avoid committing editor temp placeholders.
	// We now allow empty files so that newly created placeholder scripts/configs are synced.
	// If a user wants to exclude them they should rely on .gitignore patterns instead.

	return isFileIgnoredByGit(repoPath, filePath)
}

func isFileIgnoredByGit(repoPath string, filePath string) (bool, error) {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	patterns, err := gitignore.ReadPatterns(w.Filesystem, nil)
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	patterns = append(patterns, w.Excludes...)
	m := gitignore.NewMatcher(patterns)

	return m.Match([]string{filePath}, false), err
}

func isEmptyFile(filePath string) (bool, error) {
	stat, err := os.Stat(filePath)

	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return stat.Size() == 0, nil
}
