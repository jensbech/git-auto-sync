package common

import (
	"context"
	"errors"
	"strings"

	"github.com/ztrue/tracerr"
)

func fetch(ctx context.Context, repoConfig RepoConfig, info repoInfo) (bool, error) {
	if info.Remote == "" || info.Remote == "." {
		return false, nil
	}

	args := []string{"fetch", info.Remote}
	if info.MergeRef != "" && strings.HasPrefix(info.Upstream, "refs/remotes/") {
		args = append(args, "+"+info.MergeRef+":"+info.Upstream)
	}
	stderr, err := gitCommandStderr(ctx, repoConfig, args)
	if err != nil {
		if isMissingRemoteRef(err) {
			if _, perr := revParse(ctx, repoConfig, info.Upstream); perr == nil {
				return false, upstreamGoneError(info)
			}
			return false, nil
		}
		return false, tracerr.Wrap(err)
	}
	return strings.Contains(stderr, "(forced update)"), nil
}

func isMissingRemoteRef(err error) bool {
	var gerr *GitError
	return errors.As(err, &gerr) && strings.Contains(gerr.Stderr, "couldn't find remote ref")
}
