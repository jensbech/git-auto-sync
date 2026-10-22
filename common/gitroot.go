package common

import (
	"context"
	"path/filepath"
	"strings"
)

func RepoRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	out, err := runGit(context.Background(), RepoConfig{RepoPath: abs}, nil, []string{"rev-parse", "--show-toplevel"}, nil)
	if err != nil {
		switch ClassifyError(err) {
		case CodeXcodeLicense:
			return "", NewCodedError(CodeXcodeLicense, xcodeLicenseMessage)
		case CodeGitMissing, CodeTimeout:
			return "", NewCodedError(ClassifyError(err), errorSummary(err))
		}
		return "", NewCodedError(CodeNotARepo, "not a git repository: "+abs)
	}
	root := strings.TrimSpace(out.String())
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Clean(root), nil
}

func ResolvePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)
	rest := ""
	dir := abs
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
