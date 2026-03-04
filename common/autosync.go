package common

import (
	"errors"

	"github.com/gen2brain/beeep"
	"github.com/ztrue/tracerr"
)

func AutoSync(repoConfig RepoConfig) error {
	var err error
	err = ensureGitAuthor(repoConfig)
	if err != nil {
		return tracerr.Wrap(err)
	}

	err = commit(repoConfig)
	if err != nil {
		emitError(repoConfig, "commit failed: "+err.Error())
		return tracerr.Wrap(err)
	}

	err = fetch(repoConfig)
	if err != nil {
		return tracerr.Wrap(err)
	}

	err = rebase(repoConfig)
	if err != nil {
		if errors.Is(err, errRebaseFailed) {
			emitError(repoConfig, "rebase conflict")
			notifyIfNoClients(repoConfig, "Git Auto Sync - Conflict", "Could not rebase for - "+repoConfig.RepoPath)
		}
		return tracerr.Wrap(err)
	}

	err = push(repoConfig)
	if err != nil {
		emitError(repoConfig, "push failed: "+err.Error())
		return tracerr.Wrap(err)
	}

	emitEvent(repoConfig, NewEvent("synced", repoConfig.RepoPath, "", "ok"))
	return nil
}

func emitEvent(cfg RepoConfig, ev Event) {
	if cfg.Emitter != nil {
		cfg.Emitter.Emit(ev)
	}
}

func emitError(cfg RepoConfig, msg string) {
	emitEvent(cfg, NewEvent("error", cfg.RepoPath, msg, ""))
}

func notifyIfNoClients(cfg RepoConfig, title, message string) {
	if cfg.Clients != nil && cfg.Clients.HasClients() {
		return
	}
	_ = beeep.Alert(title, message, "")
}
