package common

import (
	"context"
	"errors"
	"path/filepath"
)

const (
	preflightRepoMissing   = "repo_missing"
	preflightNoHead        = "no_head"
	preflightUpstreamGone  = "upstream_gone"
	preflightUntrustedRepo = "untrusted_repo"
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
	_, err := preflight(context.Background(), cfg)
	return err
}

func isEnvironmentError(err error) bool {
	switch ClassifyError(err) {
	case CodeXcodeLicense, CodeGitMissing, CodeTimeout:
		return true
	}
	return false
}

func preflight(ctx context.Context, cfg RepoConfig) (repoInfo, error) {
	info, headRef, err := locateRepo(ctx, cfg)
	if err != nil {
		if isEnvironmentError(err) || ctx.Err() != nil {
			return repoInfo{}, err
		}
		switch {
		case ClassifyError(err) == CodeNotARepo:
			return repoInfo{}, newPreflight(preflightRepoMissing, "repository folder is missing")
		case isUntrustedRepo(err):
			return repoInfo{}, newPreflight(preflightUntrustedRepo, errorMessage("", err))
		}
		return repoInfo{}, newPreflight("missing_git", "not a git repository (no .git folder found)")
	}

	if rebasing, _ := isRebasing(info.GitDir); rebasing {
		return info, newPreflight("rebase_in_progress", "rebase in progress — resolve or abort manually")
	}

	if ok, _ := exists(filepath.Join(info.GitDir, "MERGE_HEAD")); ok {
		return info, newPreflight("merge_in_progress", "merge in progress — resolve or abort manually")
	}

	info, err = readBranchInfo(ctx, cfg, info, headRef)
	switch {
	case errors.Is(err, errDetachedHead):
		return info, newPreflight("detached_head", "detached HEAD — not on a branch")
	case errors.Is(err, errUnbornBranch):
		return info, newPreflight(preflightNoHead, "branch '"+info.Branch+"' has no commits yet; make the first commit and push it with `git push -u`")
	case err != nil:
		if isEnvironmentError(err) || ctx.Err() != nil {
			return info, err
		}
		return info, newPreflight("open_failed", "cannot open repository: "+errorSummary(err))
	}

	if !info.hasUpstream() {
		return info, newPreflight("no_upstream", "branch '"+info.Branch+"' has no upstream tracking branch")
	}

	return info, nil
}
