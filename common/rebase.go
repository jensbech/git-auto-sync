package common

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ztrue/tracerr"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
)

var errRebaseFailed = errors.New("git rebase failed")

func rebase(repoConfig RepoConfig) error {
	repoPath := repoConfig.RepoPath
	bi, err := fetchBranchInfo(repoPath)
	if err != nil {
		return tracerr.Wrap(err)
	}

	if bi.UpstreamRemote == "" || bi.UpstreamBranch == "" {
		return nil
	}

	_, rebaseErr := GitCommand(repoConfig, []string{"rebase", bi.UpstreamRemote + "/" + bi.UpstreamBranch})
	if rebaseErr == nil {
		return nil
	}

	rebaseInProgress, err := isRebasing(repoPath)
	if err != nil {
		return tracerr.Wrap(err)
	}

	var exerr *exec.ExitError
	if errors.As(rebaseErr, &exerr) && exerr.ExitCode() == 1 && rebaseInProgress {
		_, err := GitCommand(repoConfig, []string{"rebase", "--abort"})
		if err != nil {
			return tracerr.Wrap(err)
		}
		return errRebaseFailed
	}
	return tracerr.Wrap(rebaseErr)
}

func exists(name string) (bool, error) {
	_, err := os.Stat(name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

type branchInfo struct {
	CurrentBranch  string
	UpstreamRemote string
	UpstreamBranch string
}

func fetchBranchInfo(repoPath string) (branchInfo, error) {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return branchInfo{}, tracerr.Wrap(err)
	}

	config, err := repo.Config()
	if err != nil {
		return branchInfo{}, tracerr.Wrap(err)
	}

	ref, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return branchInfo{}, tracerr.Wrap(err)
	}

	currentBranchName := ref.Target().Short()
	branchConfig := config.Branches[currentBranchName]
	if branchConfig == nil {
		return branchInfo{CurrentBranch: currentBranchName}, nil
	}

	return branchInfo{
		CurrentBranch:  currentBranchName,
		UpstreamRemote: branchConfig.Remote,
		UpstreamBranch: branchConfig.Merge.Short(),
	}, nil
}

func isRebasing(repoPath string) (bool, error) {
	ra, err := exists(path.Join(repoPath, ".git", "rebase-apply"))
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	rm, err := exists(path.Join(repoPath, ".git", "rebase-merge"))
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	return ra || rm, nil
}

func readConflictFiles(cfg RepoConfig) []string {
	out, err := GitCommand(cfg, []string{"diff", "--name-only", "--diff-filter=U"})
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return nil
	}
	var files []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

func logConflict(cfg RepoConfig, files []string) {
	logDir := filepath.Join(cfg.RepoPath, ".git", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "auto-sync"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s rebase-conflict files=%s\n", time.Now().Format(time.RFC3339), strings.Join(files, ","))
}
