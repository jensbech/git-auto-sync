package common

import (
	"errors"
	"strings"
	"time"

	"github.com/ztrue/tracerr"
)

const pushAttempts = 3

func (s *syncRun) push() error {
	info := s.info
	if !info.hasUpstream() {
		return nil
	}

	for i := 1; ; i++ {
		_, err := GitCommandCtx(s.ctx, s.cfg, []string{"push", info.Remote, "HEAD:" + info.MergeRef})
		if err == nil {
			return nil
		}

		if !isRetryablePush(err) || i == pushAttempts {
			return tracerr.Wrap(err)
		}

		if err := s.fetch(); err != nil {
			return tracerr.Wrap(err)
		}
		if err := s.rebase(); err != nil {
			return err
		}
		if s.ahead == 0 {
			return nil
		}

		if !sleepCtx(s.ctx, time.Duration(i)*300*time.Millisecond) {
			return tracerr.Wrap(s.ctx.Err())
		}
	}
}

func isRetryablePush(err error) bool {
	var gerr *GitError
	if !errors.As(err, &gerr) || gerr.TimedOut {
		return false
	}
	if ClassifyError(err) != CodePushRejected && ClassifyError(err) != CodeLock {
		return false
	}
	s := gerr.Stderr
	return strings.Contains(s, "[rejected]") ||
		strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "cannot lock ref")
}
