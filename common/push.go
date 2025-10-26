package common

import (
	"strings"
	"time"

	"github.com/ztrue/tracerr"
)

func push(repoConfig RepoConfig) error {
	bi, err := fetchBranchInfo(repoConfig.RepoPath)
	if err != nil {
		return tracerr.Wrap(err)
	}

	if bi.UpstreamBranch == "" || bi.UpstreamRemote == "" {
		return nil
	}

	// Retry loop
	attempts := 3
	for i := 1; i <= attempts; i++ {
		_, err = GitCommand(repoConfig, []string{"push", bi.UpstreamRemote, bi.UpstreamBranch})
		if err == nil {
			return nil
		}

		// Inspect error string
		es := err.Error()
		retryable := false
		if strings.Contains(es, "non-fast-forward") ||
			strings.Contains(es, "cannot lock ref") ||
			strings.Contains(es, "failed to push some refs") {
			retryable = true
		}

		if !retryable || i == attempts {
			return tracerr.Wrap(err)
		}

		// Attempt recovery: fetch + rebase then retry
		if _, fErr := GitCommand(repoConfig, []string{"fetch", bi.UpstreamRemote}); fErr != nil {
			// If fetch fails, break early
			return tracerr.Wrap(err)
		}
		_, _ = GitCommand(repoConfig, []string{"rebase", bi.UpstreamRemote + "/" + bi.UpstreamBranch})

		// Backoff a little before retry
		time.Sleep(time.Duration(i) * 300 * time.Millisecond)
	}
	return nil
}
