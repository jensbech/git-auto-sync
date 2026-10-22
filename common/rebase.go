package common

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ztrue/tracerr"
)

var errRebaseFailed = errors.New("git rebase failed")
var errDirtyWorktree = errors.New("worktree changed during sync")
var errDetachedHead = errors.New("detached HEAD")
var errUnbornBranch = errors.New("branch has no commits")

type rebaseConflictError struct {
	Files    []string
	Head     string
	Upstream string
}

func (e *rebaseConflictError) Error() string { return errRebaseFailed.Error() }

func (e *rebaseConflictError) Is(target error) bool { return target == errRebaseFailed }

type repoInfo struct {
	GitDir        string
	CommonDir     string
	Branch        string
	Head          string
	Remote        string
	MergeRef      string
	Upstream      string
	UpstreamShort string
}

func (i repoInfo) hasUpstream() bool {
	return i.Remote != "" && i.MergeRef != "" && i.Upstream != ""
}

func upstreamGoneError(info repoInfo) error {
	branch := strings.TrimPrefix(info.MergeRef, "refs/heads/")
	return newPreflight(preflightUpstreamGone, fmt.Sprintf(
		"upstream branch '%s' no longer exists on the remote; recreate it with `git push -u %s HEAD:%s` or track another branch with `git branch -u`",
		info.UpstreamShort, info.Remote, branch))
}

func rebase(ctx context.Context, repoConfig RepoConfig, info repoInfo, forkPoint bool) error {
	if !info.hasUpstream() {
		return nil
	}

	ctx = context.WithoutCancel(ctx)
	args := []string{"rebase"}
	if forkPoint {
		args = append(args, "--fork-point")
	}
	_, rebaseErr := GitCommandCtx(ctx, repoConfig, append(args, info.Upstream))
	if rebaseErr == nil {
		return nil
	}

	rebaseInProgress, err := isRebasing(info.GitDir)
	if err != nil {
		return tracerr.Wrap(err)
	}
	if !rebaseInProgress {
		var gerr *GitError
		if errors.As(rebaseErr, &gerr) && isDirtyWorktreeMessage(gerr.Stderr) {
			return errDirtyWorktree
		}
		return tracerr.Wrap(rebaseErr)
	}

	var files []string
	if ClassifyError(rebaseErr) != CodeTimeout {
		files = readConflictFiles(ctx, repoConfig)
	}
	if _, err := GitCommandCtx(ctx, repoConfig, []string{"rebase", "--abort"}); err != nil {
		return tracerr.Wrap(fmt.Errorf("rebase failed and could not be aborted, repo is mid-rebase: %w", err))
	}
	if gitExitCode(rebaseErr) != 1 || ClassifyError(rebaseErr) == CodeTimeout || len(files) == 0 {
		return tracerr.Wrap(rebaseErr)
	}

	conflict := &rebaseConflictError{Files: files}
	conflict.Head, _ = revParse(ctx, repoConfig, "HEAD")
	conflict.Upstream, _ = revParse(ctx, repoConfig, info.Upstream)
	return conflict
}

func isDirtyWorktreeMessage(stderr string) bool {
	return strings.Contains(stderr, "You have unstaged changes") ||
		strings.Contains(stderr, "Your index contains uncommitted changes") ||
		strings.Contains(stderr, "would be overwritten by checkout")
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

func locateRepo(ctx context.Context, cfg RepoConfig) (repoInfo, string, error) {
	out, err := GitCommandCtx(ctx, cfg, []string{"rev-parse", "--absolute-git-dir", "--git-common-dir", "--symbolic-full-name", "HEAD"})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 || lines[0] == "" {
		if err == nil {
			err = tracerr.Errorf("unexpected rev-parse output: %q", out.String())
		}
		return repoInfo{}, "", err
	}
	info := repoInfo{GitDir: lines[0], CommonDir: lines[1]}
	if !filepath.IsAbs(info.CommonDir) {
		info.CommonDir = filepath.Join(cfg.RepoPath, info.CommonDir)
	}
	if err == nil && len(lines) >= 3 {
		return info, lines[2], nil
	}
	if gitExitCode(err) != 128 {
		return info, "", err
	}
	out, err = GitCommandCtx(ctx, cfg, []string{"symbolic-ref", "-q", "HEAD"})
	if err != nil {
		if gitExitCode(err) == 1 {
			return info, "HEAD", nil
		}
		return info, "", err
	}
	return info, strings.TrimSpace(out.String()), nil
}

func readRepoInfo(ctx context.Context, cfg RepoConfig) (repoInfo, error) {
	info, headRef, err := locateRepo(ctx, cfg)
	if err != nil {
		return info, err
	}
	return readBranchInfo(ctx, cfg, info, headRef)
}

func readBranchInfo(ctx context.Context, cfg RepoConfig, info repoInfo, headRef string) (repoInfo, error) {
	if !strings.HasPrefix(headRef, "refs/heads/") {
		return info, errDetachedHead
	}
	info.Branch = strings.TrimPrefix(headRef, "refs/heads/")

	out, err := GitCommandCtx(ctx, cfg, []string{
		"for-each-ref",
		"--format=%(objectname)%00%(upstream:remotename)%00%(upstream:remoteref)%00%(upstream)%00%(upstream:short)",
		"refs/heads/" + info.Branch,
	})
	if err != nil {
		return info, err
	}
	line := strings.TrimRight(out.String(), "\n")
	if line == "" {
		return info, errUnbornBranch
	}
	fields := strings.Split(line, "\x00")
	for len(fields) < 5 {
		fields = append(fields, "")
	}
	info.Head = fields[0]
	info.Remote = fields[1]
	info.MergeRef = fields[2]
	info.Upstream = fields[3]
	info.UpstreamShort = fields[4]
	return info, nil
}

func isRebasing(gitDir string) (bool, error) {
	ra, err := exists(filepath.Join(gitDir, "rebase-apply"))
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	rm, err := exists(filepath.Join(gitDir, "rebase-merge"))
	if err != nil {
		return false, tracerr.Wrap(err)
	}

	return ra || rm, nil
}

func upstreamConfigured(ctx context.Context, cfg RepoConfig, branch string) bool {
	if branch == "" {
		return false
	}
	out, err := GitCommandCtx(ctx, cfg, []string{"config", "--get-regexp", `^branch\.` + regexp.QuoteMeta(branch) + `\.(remote|merge)$`})
	if err != nil {
		return false
	}
	return len(strings.Split(strings.TrimSpace(out.String()), "\n")) >= 2
}

func remoteHasRefs(ctx context.Context, cfg RepoConfig, remote string) bool {
	if remote == "" || remote == "." {
		return true
	}
	out, err := GitCommandCtx(ctx, cfg, []string{"for-each-ref", "--count=1", "--format=%(refname)", "refs/remotes/" + remote + "/"})
	return err != nil || strings.TrimSpace(out.String()) != ""
}

func aheadBehind(ctx context.Context, cfg RepoConfig, info repoInfo) (int, int, error) {
	upstream := info.Upstream
	if upstream == "" {
		return 0, 0, nil
	}
	out, err := GitCommandCtx(ctx, cfg, []string{"rev-list", "--left-right", "--count", "HEAD..." + upstream, "--"})
	if err != nil {
		if _, perr := revParse(ctx, cfg, upstream); perr == nil || gitExitCode(perr) != 1 {
			return 0, 0, err
		}
		if remoteHasRefs(ctx, cfg, info.Remote) {
			return 0, 0, upstreamGoneError(info)
		}
		out, err = GitCommandCtx(ctx, cfg, []string{"rev-list", "--count", "HEAD", "--"})
		if err != nil {
			return 0, 0, err
		}
		n, _ := strconv.Atoi(strings.TrimSpace(out.String()))
		return n, 0, nil
	}
	parts := strings.Fields(strings.TrimSpace(out.String()))
	if len(parts) != 2 {
		return 0, 0, tracerr.Errorf("unexpected rev-list output: %q", out.String())
	}
	ahead, _ := strconv.Atoi(parts[0])
	behind, _ := strconv.Atoi(parts[1])
	return ahead, behind, nil
}

func readConflictFiles(ctx context.Context, cfg RepoConfig) []string {
	out, err := GitCommandCtx(ctx, cfg, []string{"diff", "--name-only", "-z", "--diff-filter=U"})
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(out.String(), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func logConflict(gitDir string, files []string) {
	if gitDir == "" {
		return
	}
	logDir := filepath.Join(gitDir, "logs")
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
