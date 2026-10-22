package common

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ztrue/tracerr"
)

const maxMessageFiles = 50

var commitHooks = []string{"pre-commit", "prepare-commit-msg", "commit-msg"}

type stagedChange struct {
	Status string
	Paths  []string
}

type hookError struct {
	Hook string
	Err  error
}

func (e *hookError) Error() string {
	return "commit rejected by the " + e.Hook + " hook: " + errorSummary(e.Err)
}

func (e *hookError) Unwrap() error { return e.Err }

func commit(ctx context.Context, repoConfig RepoConfig) (bool, error) {
	repoPath := repoConfig.RepoPath

	dirty, err := worktreeDirty(ctx, repoConfig)
	if err != nil {
		return false, tracerr.Wrap(err)
	}
	if !dirty {
		return false, nil
	}

	if _, err := GitCommandCtx(ctx, repoConfig, []string{"add", "-A"}); err != nil {
		return false, tracerr.Wrap(err)
	}

	changes, err := stagedChanges(ctx, repoConfig)
	if err != nil {
		return false, tracerr.Wrap(err)
	}
	if len(changes) == 0 {
		return false, nil
	}

	var tempPaths []string
	for _, c := range changes {
		for _, p := range c.Paths {
			if isTempFileName(path.Base(p)) {
				tempPaths = append(tempPaths, p)
			}
		}
	}

	if len(tempPaths) > 0 {
		args := append([]string{"--literal-pathspecs", "reset", "-q", "--"}, tempPaths...)
		if _, err := GitCommandCtx(ctx, repoConfig, args); err != nil {
			return false, tracerr.Wrap(err)
		}
		changes, err = stagedChanges(ctx, repoConfig)
		if err != nil {
			return false, tracerr.Wrap(err)
		}
		if len(changes) == 0 {
			return false, nil
		}
	}

	msg := commitMessage(changes, repoConfig.MessagePrefix)
	if _, err := GitCommandCtx(ctx, repoConfig, []string{"-c", "user.useConfigOnly=true", "commit", "-q", "-m", msg}); err != nil {
		return false, commitFailure(ctx, repoConfig, err)
	}
	log.Printf("git-auto-sync: committed %d file(s) repo=%s\n%s", len(changes), repoPath, msg)
	emitEvent(repoConfig, NewEvent(EventCommit, repoPath, msg, ""))
	return true, nil
}

func commitMessage(changes []stagedChange, prefix string) string {
	shown := changes
	if len(shown) > maxMessageFiles {
		shown = shown[:maxMessageFiles]
	}
	lines := make([]string, 0, len(shown)+1)
	for _, c := range shown {
		lines = append(lines, c.Status+" "+c.Paths[len(c.Paths)-1])
	}
	if extra := len(changes) - len(shown); extra > 0 {
		lines = append(lines, fmt.Sprintf("… and %d more", extra))
	}
	msg := strings.Join(lines, "\n")
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		msg = prefix + ": " + msg
	}
	return msg
}

func commitFailure(ctx context.Context, repoConfig RepoConfig, err error) error {
	if aerr := authorError(err); aerr != nil {
		return aerr
	}
	if ctx.Err() != nil || isEnvironmentError(err) || ClassifyError(err) == CodeLock {
		return tracerr.Wrap(err)
	}
	if changes, serr := stagedChanges(ctx, repoConfig); serr == nil && len(changes) == 0 {
		return nil
	}
	if gitExitCode(err) == 1 {
		if hook := installedCommitHook(ctx, repoConfig); hook != "" {
			return &hookError{Hook: hook, Err: err}
		}
	}
	return tracerr.Wrap(err)
}

func installedCommitHook(ctx context.Context, repoConfig RepoConfig) string {
	args := []string{"rev-parse"}
	for _, h := range commitHooks {
		args = append(args, "--git-path", "hooks/"+h)
	}
	out, err := GitCommandCtx(ctx, repoConfig, args)
	if err != nil {
		return ""
	}
	var found []string
	for i, p := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if i >= len(commitHooks) || p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoConfig.RepoPath, p)
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			found = append(found, commitHooks[i])
		}
	}
	return strings.Join(found, "/")
}

func isHookError(err error) bool {
	var he *hookError
	return errors.As(err, &he)
}

func worktreeDirty(ctx context.Context, repoConfig RepoConfig) (bool, error) {
	out, err := GitCommandCtx(ctx, repoConfig, []string{"--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=normal", "--ignore-submodules=dirty"})
	if err != nil {
		return false, err
	}
	return hasCommittableEntries(out.String()), nil
}

func hasCommittableEntries(raw string) bool {
	toks := strings.Split(raw, "\x00")
	for i := 0; i < len(toks); i++ {
		entry := toks[i]
		if len(entry) < 4 {
			continue
		}
		xy, p := entry[:2], entry[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		if xy == "??" && isTempFileName(path.Base(strings.TrimSuffix(p, "/"))) {
			continue
		}
		return true
	}
	return false
}

func stagedChanges(ctx context.Context, repoConfig RepoConfig) ([]stagedChange, error) {
	out, err := GitCommandCtx(ctx, repoConfig, []string{"diff", "--cached", "--name-status", "-z"})
	if err != nil {
		return nil, err
	}
	return parseNameStatusZ(out.String()), nil
}

func parseNameStatusZ(raw string) []stagedChange {
	toks := strings.Split(raw, "\x00")
	var changes []stagedChange
	for i := 0; i < len(toks); {
		status := toks[i]
		if status == "" {
			i++
			continue
		}
		n := 1
		if status[0] == 'R' || status[0] == 'C' {
			n = 2
		}
		if i+n >= len(toks) {
			break
		}
		changes = append(changes, stagedChange{Status: status, Paths: append([]string{}, toks[i+1:i+1+n]...)})
		i += 1 + n
	}
	return changes
}
