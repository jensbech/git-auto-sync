package common

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ztrue/tracerr"
)

func commit(repoConfig RepoConfig) error {
    repoPath := repoConfig.RepoPath

    // Stage everything (adds, mods, deletions, renames)
    if _, err := GitCommand(repoConfig, []string{"add", "-A"}); err != nil {
        return tracerr.Wrap(err)
    }

    // Check if anything is staged; if not, exit early
    // 'git diff --cached --quiet' exits 1 when there are differences
    _, err := GitCommand(repoConfig, []string{"diff", "--cached", "--quiet"})
    if err == nil {
        // Exit status 0 => no staged changes
        return nil
    }

    // Collect staged file list for commit message
    out, err := GitCommand(repoConfig, []string{"diff", "--cached", "--name-status"})
    if err != nil {
        return tracerr.Wrap(err)
    }
    raw := strings.TrimSpace(out.String())
    if raw == "" { // defensive
        return nil
    }

    // Filter ignored files (respect existing ShouldIgnoreFile logic)
    lines := []string{}
    for _, line := range strings.Split(raw, "\n") {
        parts := strings.Fields(line)
        if len(parts) < 2 { // malformed line, keep it
            lines = append(lines, line)
            continue
        }
        statusCode := parts[0]
        filePath := parts[len(parts)-1] // In rename lines last token is new path
        ignore, igErr := ShouldIgnoreFile(repoPath, filePath)
        if igErr != nil {
            return tracerr.Wrap(igErr)
        }
        if ignore {
            continue
        }
        lines = append(lines, statusCode+" "+filePath)
    }

    if len(lines) == 0 {
        // All staged files ignored => unstage them to avoid future confusion
        _, _ = GitCommand(repoConfig, []string{"reset"})
        return nil
    }

    msg := strings.Join(lines, "\n")
    if _, err := GitCommand(repoConfig, []string{"commit", "-m", msg}); err != nil {
        return tracerr.Wrap(err)
    }
    return nil
}

func GitCommand(repoConfig RepoConfig, args []string) (bytes.Buffer, error) {
	repoPath := repoConfig.RepoPath
	var outb, errb bytes.Buffer

	cmd := "git"
	if repoConfig.GitExec != "" {
		cmd = repoConfig.GitExec
	}

	if isGitHubRepo(repoPath) {
		args = append([]string{"-c", "credential.https://github.com.helper=!gh auth git-credential"}, args...)
	}

	lockPath := filepath.Join(repoPath, ".git", "index.lock")
	maxAttempts := 5
	backoff := 120 * time.Millisecond

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		outb.Reset()
		errb.Reset()

		statusCmd := exec.Command(cmd, args...)
		statusCmd.Dir = repoPath
		statusCmd.Stdout = &outb
		statusCmd.Stderr = &errb
		statusCmd.Env = toEnvString(repoConfig)
		runErr := statusCmd.Run()

		if hasEnvVariable(os.Environ(), "SSH_AUTH_SOCK") && !hasEnvVariable(repoConfig.Env, "SSH_AUTH_SOCK") && repoUsesSSH(repoConfig.RepoPath) {
			fmt.Println("WARNING: SSH_AUTH_SOCK env variable isn't being passed")
		}

		if runErr == nil {
			return outb, nil
		}

		stderr := errb.String()
		// index.lock contention detection
		if strings.Contains(stderr, "index.lock") && strings.Contains(stderr, "Unable to create") {
			// Determine staleness
			info, statErr := os.Stat(lockPath)
			stale := false
			if statErr == nil {
				if time.Since(info.ModTime()) > 4*time.Second {
					stale = true
				}
			} else if errors.Is(statErr, os.ErrNotExist) {
				// vanished between attempts, just retry
			}

			if stale {
				_ = os.Remove(lockPath) // attempt force removal
			}

			if attempt < maxAttempts {
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
		}

		fullCmd := "git " + strings.Join(args, " ")
		wrapped := tracerr.Errorf("%w: Command: %s\nEnv: %s\nStdOut: %s\nStdErr: %s", runErr, fullCmd, statusCmd.Env, outb.String(), stderr)
		return outb, wrapped
	}
	return outb, tracerr.Errorf("git command failed after retries: %s", strings.Join(args, " "))
}

func toEnvString(repoConfig RepoConfig) []string {
	vals := repoConfig.Env
	vals = append(vals, repoConfig.Env...)

	// Include essential environment variables for Git operations
	essentialVars := []string{"HOME", "PATH", "USER", "LOGNAME"}
	for _, s := range os.Environ() {
		parts := strings.Split(s, "=")
		k := parts[0]
		for _, essential := range essentialVars {
			if k == essential {
				vals = append(vals, s)
				break
			}
		}
	}

	return vals
}

func hasEnvVariable(all []string, name string) bool {
	for _, s := range all {
		parts := strings.Split(s, "=")
		k := parts[0]
		if k == name {
			return true
		}
	}
	return false
}

func repoUsesSSH(repoPath string) bool {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	remoteURL := strings.TrimSpace(string(output))
	return strings.HasPrefix(remoteURL, "git@") || strings.HasPrefix(remoteURL, "ssh://")
}

func isGitHubRepo(repoPath string) bool {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	remoteURL := strings.TrimSpace(string(output))
	return strings.Contains(remoteURL, "github.com")
}
