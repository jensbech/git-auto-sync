package common

import (
	"errors"
	"os"
	"path/filepath"

	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
)

type PreflightError struct {
	Code    string
	Message string
}

func (e *PreflightError) Error() string { return e.Message }

func preflightCode(err error) string {
	var pe *PreflightError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func newPreflight(code, msg string) error {
	return &PreflightError{Code: code, Message: msg}
}

func Preflight(cfg RepoConfig) error {
	repoPath := cfg.RepoPath

	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return newPreflight("missing_git", "not a git repo (no .git directory)")
	}

	if rebasing, _ := isRebasing(repoPath); rebasing {
		return newPreflight("rebase_in_progress", "rebase in progress — resolve or abort manually")
	}

	if _, err := os.Stat(filepath.Join(repoPath, ".git", "MERGE_HEAD")); err == nil {
		return newPreflight("merge_in_progress", "merge in progress — resolve or abort manually")
	}

	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return newPreflight("open_failed", "cannot open repository: "+err.Error())
	}

	ref, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return newPreflight("no_head", "no HEAD reference")
	}

	if ref.Type() == plumbing.HashReference {
		return newPreflight("detached_head", "detached HEAD — not on a branch")
	}

	config, err := repo.Config()
	if err != nil {
		return newPreflight("config_failed", "cannot read git config: "+err.Error())
	}

	branchName := ref.Target().Short()
	branchConfig := config.Branches[branchName]
	if branchConfig == nil || branchConfig.Remote == "" || branchConfig.Merge == "" {
		return newPreflight("no_upstream", "branch '"+branchName+"' has no upstream tracking branch")
	}

	return nil
}
