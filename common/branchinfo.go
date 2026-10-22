package common

import (
	"context"
	"strconv"
	"strings"
)

type LocalBranchInfo struct {
	Branch   string
	Upstream string
	Ahead    int
	Behind   int
}

func ReadLocalBranchInfo(ctx context.Context, cfg RepoConfig) (LocalBranchInfo, error) {
	var info LocalBranchInfo
	out, err := GitCommandCtx(ctx, cfg, []string{"symbolic-ref", "-q", "--short", "HEAD"})
	if err != nil {
		return info, err
	}
	info.Branch = strings.TrimSpace(out.String())
	if info.Branch == "" {
		return info, nil
	}

	out, err = GitCommandCtx(ctx, cfg, []string{
		"for-each-ref",
		"--format=%(upstream:short)%00%(upstream)",
		"refs/heads/" + info.Branch,
	})
	if err != nil {
		return info, err
	}
	short, full, _ := strings.Cut(strings.TrimRight(out.String(), "\n"), "\x00")
	if full == "" {
		return info, nil
	}
	info.Upstream = short

	out, err = GitCommandCtx(ctx, cfg, []string{"rev-list", "--left-right", "--count", "HEAD..." + full, "--"})
	if err != nil {
		return info, nil
	}
	parts := strings.Fields(out.String())
	if len(parts) == 2 {
		info.Ahead, _ = strconv.Atoi(parts[0])
		info.Behind, _ = strconv.Atoi(parts[1])
	}
	return info, nil
}

func RestoreBranchInfo(ctx context.Context, cfg RepoConfig) bool {
	if cfg.State == nil {
		return false
	}
	info, err := ReadLocalBranchInfo(ctx, cfg)
	if err != nil || info.Branch == "" {
		Debugf("watch: branch info unavailable repo=%s err=%v", cfg.RepoPath, err)
		return false
	}
	cfg.State.SetBranchInfo(cfg.RepoPath, info.Branch, info.Upstream, info.Ahead, info.Behind)
	return true
}
