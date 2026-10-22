package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ztrue/tracerr"
)

var (
	gitNetworkTimeout = 2 * time.Minute
	gitWriteTimeout   = 2 * time.Minute
	gitDefaultTimeout = 30 * time.Second
	gitKillDelay      = 5 * time.Second
	gitStaleLockAge   = 5 * time.Minute
)

type GitError struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Timeout  time.Duration
	Err      error
}

func (e *GitError) Error() string {
	return fmt.Sprintf("%v: Command: git %s\nStdOut: %s\nStdErr: %s", e.Err, strings.Join(e.Args, " "), e.Stdout, e.Stderr)
}

func (e *GitError) Unwrap() error { return e.Err }

func GitCommand(repoConfig RepoConfig, args []string) (bytes.Buffer, error) {
	return GitCommandCtx(context.Background(), repoConfig, args)
}

func GitCommandCtx(ctx context.Context, repoConfig RepoConfig, args []string) (bytes.Buffer, error) {
	return runGit(ctx, repoConfig, gitPrefix(ctx, repoConfig, args), args, nil)
}

func gitCommandStderr(ctx context.Context, repoConfig RepoConfig, args []string) (string, error) {
	_, stderr, err := runGitFull(ctx, repoConfig, gitPrefix(ctx, repoConfig, args), args, nil)
	return stderr, err
}

func gitPrefix(ctx context.Context, repoConfig RepoConfig, args []string) []string {
	if !isNetworkCommand(gitSubcommand(args)) {
		return nil
	}
	prefix := []string{"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=30"}
	if remoteFactsFor(ctx, repoConfig).github {
		prefix = append(prefix, "-c", "credential.https://github.com.helper=!gh auth git-credential")
	}
	return prefix
}

func runGit(ctx context.Context, repoConfig RepoConfig, prefix, args []string, stdin []byte) (bytes.Buffer, error) {
	out, _, err := runGitFull(ctx, repoConfig, prefix, args, stdin)
	return out, err
}

func runGitFull(ctx context.Context, repoConfig RepoConfig, prefix, args []string, stdin []byte) (bytes.Buffer, string, error) {
	var outb, errb bytes.Buffer

	cmdName := "git"
	if repoConfig.GitExec != "" {
		cmdName = repoConfig.GitExec
	}

	fullArgs := append(append([]string{}, prefix...), args...)
	timeout := gitTimeout(args)
	env := toEnvString(repoConfig)
	backoff := 120 * time.Millisecond
	maxAttempts := 5

	if repoConfig.RepoPath != "" {
		if _, err := os.Stat(repoConfig.RepoPath); err != nil {
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				pathErr.Op = "chdir"
			}
			return outb, "", tracerr.Wrap(&GitError{Args: args, ExitCode: -1, Timeout: timeout, Err: err})
		}
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		outb.Reset()
		errb.Reset()

		runCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(runCtx, cmdName, fullArgs...)
		cmd.Dir = repoConfig.RepoPath
		cmd.Stdout = &outb
		cmd.Stderr = &errb
		cmd.Env = env
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		cmd.SysProcAttr = gitSysProcAttr()
		cmd.Cancel = func() error { return terminateGit(cmd.Process) }
		cmd.WaitDelay = gitKillDelay
		runErr := cmd.Run()
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()

		if runErr == nil {
			return outb, errb.String(), nil
		}

		stderr := errb.String()
		if !timedOut && ctx.Err() == nil && isIndexLockError(stderr) && attempt < maxAttempts {
			removeStaleLock(lockPathFromStderr(stderr, repoConfig.RepoPath))
			if !sleepCtx(ctx, backoff) {
				break
			}
			backoff *= 2
			continue
		}

		gerr := &GitError{
			Args:     args,
			Stdout:   outb.String(),
			Stderr:   stderr,
			ExitCode: -1,
			TimedOut: timedOut,
			Timeout:  timeout,
			Err:      runErr,
		}
		var exerr *exec.ExitError
		if errors.As(runErr, &exerr) {
			gerr.ExitCode = exerr.ExitCode()
		}
		if timedOut {
			gerr.Err = fmt.Errorf("git %s timed out after %s: %w", gitSubcommand(args), timeout, context.DeadlineExceeded)
		} else if ctx.Err() != nil {
			gerr.Err = fmt.Errorf("git %s cancelled: %w", gitSubcommand(args), ctx.Err())
		}
		return outb, stderr, tracerr.Wrap(gerr)
	}
	return outb, errb.String(), tracerr.Errorf("git command failed after retries: %s", strings.Join(args, " "))
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-c" || a == "-C" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

func isNetworkCommand(sub string) bool {
	switch sub {
	case "fetch", "push", "pull", "clone", "ls-remote":
		return true
	}
	return false
}

func gitTimeout(args []string) time.Duration {
	sub := gitSubcommand(args)
	switch {
	case isNetworkCommand(sub):
		return gitNetworkTimeout
	case sub == "commit" || sub == "rebase" || sub == "add":
		return gitWriteTimeout
	}
	return gitDefaultTimeout
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func isIndexLockError(stderr string) bool {
	return strings.Contains(stderr, "index.lock") && strings.Contains(stderr, "Unable to create")
}

func lockPathFromStderr(stderr, repoPath string) string {
	const marker = "Unable to create '"
	if i := strings.Index(stderr, marker); i >= 0 {
		rest := stderr[i+len(marker):]
		if j := strings.Index(rest, "'"); j > 0 {
			return rest[:j]
		}
	}
	return filepath.Join(repoPath, ".git", "index.lock")
}

func removeStaleLock(lockPath string) {
	info, err := os.Stat(lockPath)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) > gitStaleLockAge {
		log.Printf("git-auto-sync: removing stale lock %s (age %s)", lockPath, time.Since(info.ModTime()).Round(time.Second))
		_ = os.Remove(lockPath)
	}
}

var inheritedEnvVars = map[string]bool{
	"HOME":            true,
	"USER":            true,
	"LOGNAME":         true,
	"SSH_AUTH_SOCK":   true,
	"LANG":            true,
	"XDG_CONFIG_HOME": true,
	"HTTP_PROXY":      true,
	"HTTPS_PROXY":     true,
	"ALL_PROXY":       true,
	"NO_PROXY":        true,
	"http_proxy":      true,
	"https_proxy":     true,
	"all_proxy":       true,
	"no_proxy":        true,
}

var repoLocationEnvVars = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_PREFIX":                       true,
	"GIT_NAMESPACE":                    true,
	"GIT_TERMINAL_PROMPT":              true,
}

func toEnvString(repoConfig RepoConfig) []string {
	vals := []string{"PATH=" + augmentPath(os.Getenv("PATH"))}
	for _, s := range os.Environ() {
		k, _, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		if inheritedEnvVars[k] || (strings.HasPrefix(k, "GIT_") && !repoLocationEnvVars[k]) {
			vals = append(vals, s)
		}
	}
	vals = append(vals, repoConfig.Env...)
	vals = append(vals, "GIT_TERMINAL_PROMPT=0", "LC_MESSAGES=C")
	return vals
}

func augmentPath(p string) string {
	if runtime.GOOS == "windows" {
		return p
	}
	if p == "" {
		p = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	parts := filepath.SplitList(p)
	for _, extra := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		found := false
		for _, part := range parts {
			if filepath.Clean(part) == extra {
				found = true
				break
			}
		}
		if !found {
			parts = append(parts, extra)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func hasEnvVariable(all []string, name string) bool {
	for _, s := range all {
		k, v, _ := strings.Cut(s, "=")
		if k == name && v != "" {
			return true
		}
	}
	return false
}

type remoteFacts struct {
	github bool
	ssh    bool
	stamp  string
}

var (
	remoteFactsMu    sync.Mutex
	remoteFactsCache = map[string]remoteFacts{}
	sshWarned        = map[string]bool{}
)

func ensureRemoteFacts(ctx context.Context, cfg RepoConfig, commonDir string) {
	stamp := configStamp(commonDir)
	remoteFactsMu.Lock()
	facts, ok := remoteFactsCache[cfg.RepoPath]
	remoteFactsMu.Unlock()
	if ok && stamp != "" && facts.stamp == stamp {
		return
	}
	refreshRemoteFacts(ctx, cfg, stamp)
}

func configStamp(commonDir string) string {
	if commonDir == "" {
		return ""
	}
	fi, err := os.Stat(filepath.Join(commonDir, "config"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
}

func refreshRemoteFacts(ctx context.Context, cfg RepoConfig, stamp string) remoteFacts {
	facts := remoteFacts{stamp: stamp}
	out, err := runGit(ctx, cfg, nil, []string{"config", "--get-regexp", `^remote\..*\.url$`}, nil)
	if err == nil {
		for _, line := range strings.Split(out.String(), "\n") {
			_, url, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok {
				continue
			}
			if strings.Contains(url, "github.com") {
				facts.github = true
			}
			if strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://") {
				facts.ssh = true
			}
		}
	}

	remoteFactsMu.Lock()
	remoteFactsCache[cfg.RepoPath] = facts
	warn := facts.ssh && !sshWarned[cfg.RepoPath] && !hasEnvVariable(toEnvString(cfg), "SSH_AUTH_SOCK")
	if warn {
		sshWarned[cfg.RepoPath] = true
	}
	remoteFactsMu.Unlock()

	if warn {
		log.Printf("git-auto-sync: warning repo=%s uses an SSH remote but SSH_AUTH_SOCK is not set; key authentication may fail", cfg.RepoPath)
	}
	return facts
}

func remoteFactsFor(ctx context.Context, cfg RepoConfig) remoteFacts {
	remoteFactsMu.Lock()
	facts, ok := remoteFactsCache[cfg.RepoPath]
	remoteFactsMu.Unlock()
	if ok {
		return facts
	}
	return refreshRemoteFacts(ctx, cfg, "")
}

func gitExitCode(err error) int {
	var gerr *GitError
	if errors.As(err, &gerr) {
		return gerr.ExitCode
	}
	var exerr *exec.ExitError
	if errors.As(err, &exerr) {
		return exerr.ExitCode()
	}
	return -1
}

func revParse(ctx context.Context, cfg RepoConfig, ref string) (string, error) {
	out, err := GitCommandCtx(ctx, cfg, []string{"rev-parse", "--verify", "-q", ref + "^{commit}"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func isAncestor(ctx context.Context, cfg RepoConfig, ancestor, descendant string) bool {
	_, err := GitCommandCtx(ctx, cfg, []string{"merge-base", "--is-ancestor", ancestor, descendant})
	return err == nil
}
