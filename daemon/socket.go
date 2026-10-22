package main

import (
	"errors"
	"log"
	"path/filepath"
	"sort"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
)

var version = "dev"

const socketInUseWait = 15 * time.Second

func (d *Daemon) startSocket() {
	if _, err := common.EnsureStateDir(); err != nil {
		log.Printf("daemon: state dir: %v", err)
	}

	socketPath := common.DefaultSocketPath()
	sock, err := common.NewSocketServer(socketPath, d)
	deadline := time.Now().Add(socketInUseWait)
	for errors.Is(err, common.ErrSocketInUse) && time.Now().Before(deadline) {
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		sock, err = common.NewSocketServer(socketPath, d)
	}
	if errors.Is(err, common.ErrSocketInUse) {
		log.Fatalf("daemon: %v; exiting so two daemons do not sync the same repos", err)
	}
	if err != nil {
		log.Printf("daemon: socket server failed to start: %v (continuing without socket)", err)
		return
	}

	events, err := common.NewEventLog(common.EventsPath(), common.EventLogCapacity)
	if err != nil {
		log.Printf("daemon: event log unavailable, keeping history in memory only: %v", err)
	} else {
		sock.SetEventLog(events)
	}

	d.sock = sock
	d.emitter = common.NewMultiEmitter(common.NewLogEmitter(), sock)

	sock.SetHelloProvider(func() common.Hello {
		return common.Hello{
			Version: version,
			Repos:   d.repoSnapshot(),
		}
	})

	d.state.OnChange(sock.BroadcastState)

	go sock.Accept()
}

func (d *Daemon) repoSnapshot() []common.RepoState {
	repos := d.state.Snapshot()
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	return repos
}

func (d *Daemon) lastEventID() uint64 {
	if d.sock == nil {
		return 0
	}
	return d.sock.Events().LastID()
}

func (d *Daemon) canonicalRepo(repo string) string {
	if key, _, ok := d.lookupWatcher(repo); ok {
		return key
	}
	return common.ResolvePath(repo)
}

func errResponse(err error) common.Response {
	code := common.ErrorCode(err)
	if code == "" {
		code = common.CodeUnknown
	}
	return common.ErrResponse(code, err.Error())
}

func commandOK(status, repo string) common.Response {
	return common.OKResponse(common.CommandResult{Status: status, Repo: repo})
}

func (d *Daemon) HandleCommand(req common.Request) common.Response {
	switch req.Cmd {
	case common.CmdStatus, common.CmdList:
		return common.OKResponse(common.StatusResult{
			Protocol:    common.ProtocolVersion,
			Version:     version,
			Daemon:      "running",
			Repos:       d.repoSnapshot(),
			LastEventID: d.lastEventID(),
		})

	case common.CmdVersion:
		return common.OKResponse(common.VersionResult{Protocol: common.ProtocolVersion, Version: version})

	case common.CmdEvents:
		result := common.EventsResult{Events: []common.Event{}}
		if d.sock != nil {
			repo := req.Repo
			if repo != "" {
				repo = d.canonicalRepo(filepath.Clean(repo))
			}
			result.Events = d.sock.Events().Since(req.Since, repo, req.Limit)
			result.LastEventID = d.sock.Events().LastID()
		}
		return common.OKResponse(result)

	case common.CmdSyncAll:
		d.SyncAll()
		return commandOK("triggered", "")

	case common.CmdPauseAll:
		d.SetAllPaused(true)
		return commandOK("paused", "")

	case common.CmdResumeAll:
		d.SetAllPaused(false)
		return commandOK("resumed", "")

	case common.CmdSubscribe:
		return common.ErrResponse(common.CodeBadRequest, "subscribe is only valid on a socket connection")

	case common.CmdAdd, common.CmdRemove, common.CmdSyncNow, common.CmdPause, common.CmdResume,
		common.CmdGetSettings, common.CmdSetSettings, common.CmdAbortRebase:

	default:
		return common.ErrResponse(common.CodeUnknownCmd, "unknown command: "+req.Cmd)
	}

	if req.Repo == "" {
		return common.ErrResponse(common.CodeMissingRepo, "missing repo")
	}
	if !filepath.IsAbs(req.Repo) {
		return common.ErrResponse(common.CodeBadRequest, "repo must be an absolute path: "+req.Repo)
	}
	repo := filepath.Clean(req.Repo)

	switch req.Cmd {
	case common.CmdAdd:
		root, err := d.AddRepo(repo)
		if common.ErrorCode(err) == common.CodeAlreadyExists {
			if root == "" {
				root = repo
			}
			return commandOK(common.CodeAlreadyExists, root)
		}
		if err != nil {
			return errResponse(err)
		}
		return commandOK("added", root)

	case common.CmdRemove:
		if err := d.RemoveRepo(repo); err != nil {
			return errResponse(err)
		}
		return commandOK("removed", repo)

	case common.CmdSyncNow:
		if err := d.SyncNow(repo, req.Force); err != nil {
			return errResponse(err)
		}
		return commandOK("triggered", d.canonicalRepo(repo))

	case common.CmdPause, common.CmdResume:
		paused := req.Cmd == common.CmdPause
		if err := d.SetPaused(repo, paused); err != nil {
			return errResponse(err)
		}
		if paused {
			return commandOK("paused", d.canonicalRepo(repo))
		}
		return commandOK("resumed", d.canonicalRepo(repo))

	case common.CmdAbortRebase:
		if err := d.AbortRebase(repo); err != nil {
			return errResponse(err)
		}
		return commandOK("aborted", d.canonicalRepo(repo))

	case common.CmdGetSettings:
		settings, err := d.readRepoSettings(repo)
		if err != nil {
			return errResponse(err)
		}
		return common.OKResponse(settings)

	case common.CmdSetSettings:
		if len(req.Settings) == 0 {
			return common.ErrResponse(common.CodeBadRequest, "missing settings")
		}
		if err := d.writeRepoSettings(repo, req.Settings); err != nil {
			return errResponse(err)
		}
		go d.restartWatcher(repo)
		return commandOK("saved", repo)
	}

	return common.ErrResponse(common.CodeUnknownCmd, "unknown command: "+req.Cmd)
}
