package common

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"unicode/utf8"
)

const (
	xcodeLicenseMessage = "Xcode license not accepted. Run `sudo xcodebuild -license accept`"
	gitMissingMessage   = "git is not installed or not working. Install the Xcode Command Line Tools with `xcode-select --install`"
	lfsMissingMessage   = "This repository uses Git LFS, but git-lfs is not installed. Install it with `brew install git-lfs`"
	untrustedMessage    = "Git does not trust this folder because another user owns it. Run `git config --global --add safe.directory <path>`"
	lockMessage         = "Another git process is using this repository; will retry"
	repoMissingMessage  = "Repository folder is missing or is not a git repository"
)

var (
	lfsMissingPatterns = []string{"git-lfs: command not found", "git-lfs: not found", "'git-lfs' was not found"}
	signingPatterns    = []string{"failed to sign the data", "gpg: signing failed"}
	untrustedPatterns  = []string{"dubious ownership"}
)

type errorRule struct {
	code     string
	patterns []string
}

var errorRules = []errorRule{
	{CodeXcodeLicense, []string{
		"xcode license",
		"xcodebuild -license",
	}},
	{CodeGitMissing, []string{
		"invalid active developer path",
		"no developer tools were found",
		"executable file not found",
	}},
	{CodeConfig, concatPatterns(lfsMissingPatterns, signingPatterns, untrustedPatterns, []string{"couldn't find remote ref"})},
	{CodeAuth, []string{
		"authentication failed",
		"could not read username",
		"could not read password",
		"terminal prompts disabled",
		"permission denied (publickey",
		"permission denied, please try again",
		"invalid username or password",
		"invalid credentials",
		"bad credentials",
		"password authentication was removed",
		"requested url returned error: 401",
		"requested url returned error: 403",
		"host key verification failed",
		"repository not found",
	}},
	{CodeNetwork, []string{
		"could not resolve host",
		"could not resolve hostname",
		"couldn't resolve host",
		"temporary failure in name resolution",
		"nodename nor servname",
		"name or service not known",
		"failed to connect",
		"connection refused",
		"connection timed out",
		"connection timeout",
		"operation timed out",
		"connection reset",
		"connection closed by",
		"network is unreachable",
		"no route to host",
		"requested url returned error: 5",
		"rpc failed",
		"early eof",
		"remote end hung up unexpectedly",
		"unexpected disconnect",
		"kex_exchange_identification",
		"ssl_connect",
		"ssl_error",
		"ssl routines",
		"gnutls",
		"tls connection",
		"broken pipe",
		"from proxy after connect",
	}},
	{CodeConfig, []string{
		"does not appear to be a git repository",
		"no such remote",
		"not a git repository",
		"bad config",
	}},
	{CodeLock, []string{
		"index.lock",
		"cannot lock ref",
	}},
	{CodePushRejected, []string{
		"[rejected]",
		"[remote rejected]",
		"non-fast-forward",
		"fetch first",
		"pre-receive hook declined",
		"protected branch",
		"failed to push some refs",
	}},
	{CodeNetwork, []string{
		"could not read from remote repository",
	}},
}

func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	if code := ErrorCode(err); code != "" {
		return code
	}
	if errors.Is(err, errRebaseFailed) {
		return CodeConflict
	}
	if errors.Is(err, errNoGitAuthorEmail) || errors.Is(err, errNoGitAuthorName) {
		return CodeNoAuthor
	}
	var pe *PreflightError
	if errors.As(err, &pe) {
		return CodeConfig
	}
	if isHookError(err) {
		return CodeConfig
	}

	var gerr *GitError
	isGit := errors.As(err, &gerr)
	if (isGit && gerr.TimedOut) || errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}

	text := err.Error()
	if isGit {
		text = gerr.Stderr + "\n" + gerr.Stdout
		var pathErr *fs.PathError
		if gerr.ExitCode == -1 && errors.As(gerr.Err, &pathErr) && pathErr.Op == "chdir" {
			return CodeNotARepo
		}
		if gerr.ExitCode == -1 && (errors.Is(gerr.Err, exec.ErrNotFound) || errors.Is(gerr.Err, fs.ErrNotExist)) {
			return CodeGitMissing
		}
	}
	lower := strings.ToLower(text)
	if gitExitCode(err) == 69 && strings.Contains(lower, "license") {
		return CodeXcodeLicense
	}
	for _, rule := range errorRules {
		for _, p := range rule.patterns {
			if strings.Contains(lower, p) {
				return rule.code
			}
		}
	}
	return CodeUnknown
}

func concatPatterns(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func gitErrorText(err error) string {
	var gerr *GitError
	if errors.As(err, &gerr) {
		return strings.ToLower(gerr.Stderr + "\n" + gerr.Stdout)
	}
	return strings.ToLower(err.Error())
}

func isNetworkTimeout(err error) bool {
	var gerr *GitError
	if errors.As(err, &gerr) {
		return isNetworkCommand(gitSubcommand(gerr.Args))
	}
	return true
}

func isUntrustedRepo(err error) bool {
	return err != nil && containsAny(gitErrorText(err), untrustedPatterns)
}

func ErrorMessage(err error) string {
	return errorMessage("", err)
}

func errorMessage(stage string, err error) string {
	if err == nil {
		return ""
	}
	var pe *PreflightError
	if errors.As(err, &pe) {
		return pe.Message
	}
	var he *hookError
	if errors.As(err, &he) {
		return capitalize(he.Error())
	}
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Msg
	}

	code := ClassifyError(err)
	text := gitErrorText(err)
	detail := errorSummary(err)
	switch code {
	case CodeXcodeLicense:
		return xcodeLicenseMessage
	case CodeGitMissing:
		return gitMissingMessage
	case CodeNoAuthor:
		return firstLine(err.Error())
	case CodeNotARepo:
		return repoMissingMessage
	case CodeLock:
		return lockMessage
	case CodeConflict:
		return "Rebase conflict"
	case CodeTimeout:
		return capitalize(detail)
	case CodeNetwork:
		return withDetail("Can't reach the remote", networkDetail(detail))
	case CodeAuth:
		return withDetail("Authentication failed", networkDetail(detail))
	case CodePushRejected:
		return withDetail("Push rejected", pushRejectDetail(err, detail))
	case CodeConfig:
		switch {
		case containsAny(text, lfsMissingPatterns):
			return lfsMissingMessage
		case containsAny(text, untrustedPatterns):
			return untrustedMessage
		case containsAny(text, signingPatterns):
			return withDetail("Commit signing failed (commit.gpgsign is on)", detail)
		}
	}
	return withDetail(stageLabel(stage), detail)
}

func stageLabel(stage string) string {
	switch stage {
	case "preflight":
		return "Repository check failed"
	case "commit":
		return "Commit failed"
	case "fetch":
		return "Fetch failed"
	case "rebase":
		return "Rebase failed"
	case "push":
		return "Push failed"
	}
	return "Git failed"
}

func withDetail(label, detail string) string {
	if detail == "" {
		return label
	}
	if strings.Contains(strings.ToLower(detail), strings.ToLower(label)) {
		return capitalize(detail)
	}
	return label + ": " + detail
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 || r >= utf8.RuneSelf {
		return s
	}
	return strings.ToUpper(string(r)) + s[size:]
}

func networkDetail(detail string) string {
	const marker = "unable to access '"
	if i := strings.Index(detail, marker); i >= 0 {
		rest := detail[i+len(marker):]
		if j := strings.Index(rest, "': "); j >= 0 {
			return detail[:i] + rest[j+3:]
		}
	}
	return detail
}

func pushRejectDetail(err error, detail string) string {
	var gerr *GitError
	if !errors.As(err, &gerr) {
		return detail
	}
	var remoteErrs []string
	reason := ""
	for _, line := range meaningfulLines(gerr.Stderr) {
		if strings.HasPrefix(line, "remote: error: ") {
			remoteErrs = append(remoteErrs, strings.TrimPrefix(line, "remote: error: "))
			continue
		}
		if strings.Contains(line, "[rejected]") || strings.Contains(line, "[remote rejected]") {
			if i := strings.LastIndex(line, "("); i >= 0 && strings.HasSuffix(line, ")") {
				reason = line[i+1 : len(line)-1]
			}
		}
	}
	if len(remoteErrs) > 0 {
		return truncate(strings.Join(remoteErrs, "; "), 300)
	}
	switch reason {
	case "":
		return detail
	case "fetch first", "non-fast-forward":
		return "the remote has new commits; will retry"
	}
	return reason
}

func meaningfulLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "hint:") || line == "(no gpg output)" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func errorSummary(err error) string {
	if err == nil {
		return ""
	}
	var gerr *GitError
	if errors.As(err, &gerr) {
		if gerr.TimedOut {
			return "git " + gitSubcommand(gerr.Args) + " timed out after " + gerr.Timeout.String()
		}
		lines := meaningfulLines(gerr.Stderr)
		var picked []string
		seen := map[string]bool{}
		for _, line := range lines {
			for _, p := range []string{"fatal: ", "error: ", "remote: error: ", "ERROR: "} {
				if strings.HasPrefix(line, p) {
					msg := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, p)), ":")
					if msg != "" && !seen[msg] {
						seen[msg] = true
						picked = append(picked, msg)
					}
					break
				}
			}
		}
		if len(picked) == 0 && len(lines) > 0 {
			picked = []string{lines[len(lines)-1]}
		}
		if len(picked) == 0 {
			if out := meaningfulLines(gerr.Stdout); len(out) > 0 {
				picked = []string{out[len(out)-1]}
			}
		}
		if len(picked) > 0 {
			return truncate(strings.Join(picked, "; "), 300)
		}
		if sub := gitSubcommand(gerr.Args); sub != "" {
			return "git " + sub + " failed without output"
		}
		return "git failed without output"
	}
	return truncate(firstLine(err.Error()), 300)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
