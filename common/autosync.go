package common

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gen2brain/beeep"
	"github.com/ztrue/tracerr"
)

func AutoSync(repoConfig RepoConfig) error {
	if repoConfig.State != nil {
		repoConfig.State.Ensure(repoConfig.RepoPath)
	}

	if err := Preflight(repoConfig); err != nil {
		code := preflightCode(err)
		emitPreflight(repoConfig, err.Error(), code)
		if repoConfig.State != nil {
			repoConfig.State.MarkPreflight(repoConfig.RepoPath, err.Error())
		}
		return tracerr.Wrap(err)
	}

	refreshBranchInfo(repoConfig)

	if repoConfig.State != nil {
		repoConfig.State.MarkSyncing(repoConfig.RepoPath)
	}

	var err error
	err = ensureGitAuthor(repoConfig)
	if err != nil {
		emitError(repoConfig, "author setup failed: "+err.Error())
		if repoConfig.State != nil {
			repoConfig.State.MarkError(repoConfig.RepoPath, err.Error())
		}
		return tracerr.Wrap(err)
	}

	err = commit(repoConfig)
	if err != nil {
		emitError(repoConfig, "commit failed: "+err.Error())
		if repoConfig.State != nil {
			repoConfig.State.MarkError(repoConfig.RepoPath, err.Error())
		}
		return tracerr.Wrap(err)
	}

	err = fetch(repoConfig)
	if err != nil {
		emitError(repoConfig, "fetch failed: "+err.Error())
		if repoConfig.State != nil {
			repoConfig.State.MarkError(repoConfig.RepoPath, err.Error())
		}
		return tracerr.Wrap(err)
	}

	err = rebase(repoConfig)
	if err != nil {
		if errors.Is(err, errRebaseFailed) {
			files := readConflictFiles(repoConfig)
			logConflict(repoConfig, files)
			emitConflict(repoConfig, files)
			if repoConfig.State != nil {
				repoConfig.State.MarkConflict(repoConfig.RepoPath, files, "rebase conflict")
			}
			notifyIfNoClients(repoConfig, "Git Auto Sync - Conflict", "Could not rebase for - "+repoConfig.RepoPath)
		} else {
			emitError(repoConfig, "rebase failed: "+err.Error())
			if repoConfig.State != nil {
				repoConfig.State.MarkError(repoConfig.RepoPath, err.Error())
			}
		}
		return tracerr.Wrap(err)
	}

	err = push(repoConfig)
	if err != nil {
		emitError(repoConfig, "push failed: "+err.Error())
		if repoConfig.State != nil {
			repoConfig.State.MarkError(repoConfig.RepoPath, err.Error())
		}
		return tracerr.Wrap(err)
	}

	emitEvent(repoConfig, NewEvent("synced", repoConfig.RepoPath, "", "ok"))
	if repoConfig.State != nil {
		repoConfig.State.MarkSuccess(repoConfig.RepoPath, "synced")
	}
	refreshBranchInfo(repoConfig)
	return nil
}

func refreshBranchInfo(cfg RepoConfig) {
	if cfg.State == nil {
		return
	}
	bi, err := fetchBranchInfo(cfg.RepoPath)
	if err != nil {
		return
	}
	upstream := ""
	if bi.UpstreamRemote != "" && bi.UpstreamBranch != "" {
		upstream = bi.UpstreamRemote + "/" + bi.UpstreamBranch
	}
	ahead, behind := readAheadBehind(cfg, upstream)
	cfg.State.SetBranchInfo(cfg.RepoPath, bi.CurrentBranch, upstream, ahead, behind)
}

func readAheadBehind(cfg RepoConfig, upstream string) (int, int) {
	if upstream == "" {
		return 0, 0
	}
	out, err := GitCommand(cfg, []string{"rev-list", "--left-right", "--count", "HEAD..." + upstream})
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(strings.TrimSpace(out.String()))
	if len(parts) != 2 {
		return 0, 0
	}
	ahead, _ := strconv.Atoi(parts[0])
	behind, _ := strconv.Atoi(parts[1])
	return ahead, behind
}

func emitEvent(cfg RepoConfig, ev Event) {
	if cfg.Emitter != nil {
		cfg.Emitter.Emit(ev)
	}
}

func emitError(cfg RepoConfig, msg string) {
	emitEvent(cfg, NewEvent("error", cfg.RepoPath, msg, ""))
}

func emitPreflight(cfg RepoConfig, msg, code string) {
	emitEvent(cfg, NewEvent("preflight", cfg.RepoPath, msg, code))
}

func emitConflict(cfg RepoConfig, files []string) {
	msg := "rebase conflict"
	if len(files) > 0 {
		msg = "rebase conflict: " + strings.Join(files, ", ")
	}
	emitEvent(cfg, NewEvent("conflict", cfg.RepoPath, msg, ""))
}

func notifyIfNoClients(cfg RepoConfig, title, message string) {
	if cfg.Clients != nil && cfg.Clients.HasClients() {
		return
	}
	_ = beeep.Alert(title, message, "")
}
