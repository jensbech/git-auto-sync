package common

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"testing"

	"github.com/ztrue/tracerr"
	"gotest.tools/v3/assert"
)

func gitErr(exitCode int, stderr string) error {
	return tracerr.Wrap(&GitError{
		Args:     []string{"fetch", "origin"},
		Stderr:   stderr,
		ExitCode: exitCode,
		Err:      fmt.Errorf("exit status %d", exitCode),
	})
}

func Test_ClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"repo folder deleted", tracerr.Wrap(&GitError{Args: []string{"status"}, ExitCode: -1, Err: &fs.PathError{Op: "chdir", Path: "/gone", Err: fs.ErrNotExist}}), CodeNotARepo},
		{"xcode license", gitErr(69, "You have not agreed to the Xcode license agreements. Please run 'sudo xcodebuild -license' from within a Terminal window to review and agree to the Xcode and Apple SDKs license."), CodeXcodeLicense},
		{"xcode tools missing", gitErr(1, "xcrun: error: invalid active developer path (/Library/Developer/CommandLineTools), missing xcrun at: /Library/Developer/CommandLineTools/usr/bin/xcrun"), CodeGitMissing},
		{"git binary missing", tracerr.Wrap(&GitError{ExitCode: -1, Err: &exec.Error{Name: "git", Err: exec.ErrNotFound}}), CodeGitMissing},
		{"dns", gitErr(128, "fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"), CodeNetwork},
		{"http 502", gitErr(128, "error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502\nfatal: expected flush after ref listing"), CodeNetwork},
		{"http 504", gitErr(128, "fatal: unable to access 'https://github.com/x/y.git/': The requested URL returned error: 504"), CodeNetwork},
		{"tls", gitErr(128, "fatal: unable to access 'https://github.com/x/y/': LibreSSL SSL_connect: SSL_ERROR_SYSCALL in connection to github.com:443"), CodeNetwork},
		{"slow connection", gitErr(128, "fatal: unable to access 'https://github.com/x/y/': Connection timeout after 1064984 ms"), CodeNetwork},
		{"ssh unreachable", gitErr(128, "ssh: connect to host github.com port 22: Operation timed out\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists."), CodeNetwork},
		{"connection refused", gitErr(128, "fatal: unable to access 'http://127.0.0.1:1/x.git/': Failed to connect to 127.0.0.1 port 1 after 0 ms: Couldn't connect to server"), CodeNetwork},
		{"http 403", gitErr(128, "remote: Permission to x/y.git denied to bob.\nfatal: unable to access 'https://github.com/x/y.git/': The requested URL returned error: 403"), CodeAuth},
		{"auth failed", gitErr(128, "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/x/y.git/'"), CodeAuth},
		{"no prompt", gitErr(128, "fatal: could not read Username for 'https://github.com': terminal prompts disabled"), CodeAuth},
		{"ssh key", gitErr(128, "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."), CodeAuth},
		{"push rejected", gitErr(1, "To github.com:x/y.git\n ! [rejected]        HEAD -> main (fetch first)\nerror: failed to push some refs to 'github.com:x/y.git'"), CodePushRejected},
		{"hook declined", gitErr(1, " ! [remote rejected] HEAD -> main (pre-receive hook declined)\nerror: failed to push some refs"), CodePushRejected},
		{"index lock", gitErr(128, "fatal: Unable to create '/r/.git/index.lock': File exists."), CodeLock},
		{"bad remote", gitErr(128, "fatal: 'origin' does not appear to be a git repository\nfatal: Could not read from remote repository."), CodeConfig},
		{"lfs missing is not offline", gitErr(128, "git-lfs filter-process: git-lfs: command not found\nfatal: the remote end hung up unexpectedly"), CodeConfig},
		{"lfs missing dash", gitErr(128, "sh: 1: git-lfs: not found\nfatal: the remote end hung up unexpectedly"), CodeConfig},
		{"signing", gitErr(128, "error: gpg failed to sign the data\nfatal: failed to write commit object"), CodeConfig},
		{"dubious ownership", gitErr(128, "fatal: detected dubious ownership in repository at '/r'"), CodeConfig},
		{"hook mentioning network", &hookError{Hook: "pre-commit", Err: gitErr(1, "curl: Could not resolve host: lint.example")}, CodeConfig},
		{"missing remote ref", gitErr(128, "fatal: couldn't find remote ref refs/heads/feature"), CodeConfig},
		{"unknown", gitErr(128, "fatal: something odd happened"), CodeUnknown},
		{"timeout", tracerr.Wrap(&GitError{TimedOut: true, Err: errors.New("killed")}), CodeTimeout},
		{"conflict sentinel", errRebaseFailed, CodeConflict},
		{"conflict error", tracerr.Wrap(&rebaseConflictError{Files: []string{"a.md"}}), CodeConflict},
		{"no author", tracerr.Wrap(errNoGitAuthorEmail), CodeNoAuthor},
		{"coded", NewCodedError(CodeNotARepo, "nope"), CodeNotARepo},
		{"preflight", newPreflight("no_upstream", "no upstream"), CodeConfig},
		{"plain", errors.New("boom"), CodeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, ClassifyError(tc.err), tc.want)
		})
	}
}

func Test_ErrorSummary(t *testing.T) {
	assert.Equal(t, errorSummary(gitErr(128, "hint: foo\nfatal: unable to access 'x': Could not resolve host: x\n")), "unable to access 'x': Could not resolve host: x")
	assert.Equal(t, errorSummary(gitErr(1, "something plain\n")), "something plain")
	assert.Equal(t, errorSummary(tracerr.Wrap(&GitError{Args: []string{"fetch"}, TimedOut: true, Timeout: gitNetworkTimeout, Err: errors.New("x")})), "git fetch timed out after 2m0s")
	assert.Equal(t, errorSummary(errors.New("line one\nline two")), "line one")
}

func Test_ErrorSummaryFallbacks(t *testing.T) {
	nothing := tracerr.Wrap(&GitError{Args: []string{"commit", "-q"}, Stdout: "On branch main\nnothing to commit, working tree clean\n", ExitCode: 1, Err: errors.New("exit status 1")})
	assert.Equal(t, errorSummary(nothing), "nothing to commit, working tree clean")
	silent := tracerr.Wrap(&GitError{Args: []string{"-c", "a=b", "push", "origin"}, ExitCode: 128, Err: errors.New("exit status 128")})
	assert.Equal(t, errorSummary(silent), "git push failed without output")
	progress := gitErr(1, "Rebasing (1/1)\rfatal: cannot exec 'gpg': No such file or directory\nerror: gpg failed to sign the data:\n(no gpg output)\nhint: Could not execute the todo command\n")
	assert.Equal(t, errorSummary(progress), "cannot exec 'gpg': No such file or directory; gpg failed to sign the data")
}

func Test_ErrorMessages(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		err   error
		want  string
	}{
		{"network", "fetch", gitErr(128, "fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"), "Can't reach the remote: Could not resolve host: github.com"},
		{"timeout", "fetch", tracerr.Wrap(&GitError{Args: []string{"fetch", "origin"}, TimedOut: true, Timeout: gitNetworkTimeout, Err: errors.New("killed")}), "Git fetch timed out after 2m0s"},
		{"auth", "fetch", gitErr(128, "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/x/y.git/'"), "Authentication failed for 'https://github.com/x/y.git/'"},
		{"auth ssh", "push", gitErr(128, "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."), "Authentication failed: Could not read from remote repository."},
		{"conflict", "rebase", tracerr.Wrap(&rebaseConflictError{Files: []string{"a.md"}}), "Rebase conflict"},
		{"lock", "commit", gitErr(128, "fatal: Unable to create '/r/.git/index.lock': File exists."), lockMessage},
		{"xcode", "preflight", gitErr(69, "You have not agreed to the Xcode license agreements."), xcodeLicenseMessage},
		{"git missing", "preflight", tracerr.Wrap(&GitError{ExitCode: -1, Err: &exec.Error{Name: "git", Err: exec.ErrNotFound}}), gitMissingMessage},
		{"no author", "commit", tracerr.Wrap(errNoGitAuthorEmail), errNoGitAuthorEmail.Error()},
		{"push rejected", "push", gitErr(1, "To github.com:x/y.git\n ! [remote rejected] HEAD -> main (pre-receive hook declined)\nerror: failed to push some refs to 'github.com:x/y.git'"), "Push rejected: pre-receive hook declined"},
		{"protected", "push", gitErr(1, "remote: error: GH006: Protected branch update failed for refs/heads/main.\nTo github.com:x/y.git\n ! [remote rejected] HEAD -> main (protected branch hook declined)\nerror: failed to push some refs"), "Push rejected: GH006: Protected branch update failed for refs/heads/main."},
		{"non-ff", "push", gitErr(1, " ! [rejected]        HEAD -> main (fetch first)\nerror: failed to push some refs to 'x'"), "Push rejected: the remote has new commits; will retry"},
		{"config", "fetch", gitErr(128, "fatal: 'origin' does not appear to be a git repository\nfatal: Could not read from remote repository."), "Fetch failed: 'origin' does not appear to be a git repository; Could not read from remote repository."},
		{"lfs", "commit", gitErr(128, "git-lfs filter-process: git-lfs: command not found\nfatal: the remote end hung up unexpectedly"), lfsMissingMessage},
		{"lfs hook", "push", gitErr(1, "This repository is configured for Git LFS but 'git-lfs' was not found on your path."), lfsMissingMessage},
		{"signing", "commit", gitErr(128, "fatal: cannot exec 'gpg': No such file or directory\nerror: gpg failed to sign the data:\n(no gpg output)\nfatal: failed to write commit object"), "Commit signing failed (commit.gpgsign is on): cannot exec 'gpg': No such file or directory; gpg failed to sign the data; failed to write commit object"},
		{"untrusted", "preflight", gitErr(128, "fatal: detected dubious ownership in repository at '/r'"), untrustedMessage},
		{"not a repo", "preflight", tracerr.Wrap(&GitError{Args: []string{"status"}, ExitCode: -1, Err: &fs.PathError{Op: "chdir", Path: "/gone", Err: fs.ErrNotExist}}), repoMissingMessage},
		{"hook", "commit", &hookError{Hook: "pre-commit", Err: gitErr(1, "lint failed\n")}, "Commit rejected by the pre-commit hook: lint failed"},
		{"preflight", "fetch", newPreflight(preflightUpstreamGone, "upstream gone"), "upstream gone"},
		{"coded", "", NewCodedError(CodeNotARepo, "not a git repository: /x"), "not a git repository: /x"},
		{"unknown", "commit", gitErr(1, ""), "Commit failed: git fetch failed without output"},
		{"plain", "rebase", errors.New("boom\nstack"), "Rebase failed: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := errorMessage(tc.stage, tc.err)
			assert.Equal(t, got, tc.want)
			assert.Assert(t, !strings.Contains(got, "\n"))
			assert.Assert(t, !strings.Contains(got, "exit status"))
			assert.Assert(t, !strings.Contains(got, "Command: git"))
		})
	}
	assert.Equal(t, ErrorMessage(nil), "")
}
