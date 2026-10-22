package common

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const ProtocolVersion = 2

const (
	CmdStatus      = "status"
	CmdList        = "list"
	CmdAdd         = "add"
	CmdRemove      = "remove"
	CmdSyncNow     = "sync_now"
	CmdSyncAll     = "sync_all"
	CmdPause       = "pause"
	CmdResume      = "resume"
	CmdPauseAll    = "pause_all"
	CmdResumeAll   = "resume_all"
	CmdGetSettings = "get_settings"
	CmdSetSettings = "set_settings"
	CmdAbortRebase = "abort_rebase"
	CmdEvents      = "events"
	CmdSubscribe   = "subscribe"
	CmdVersion     = "version"
)

type Request struct {
	ID       string            `json:"id,omitempty"`
	Cmd      string            `json:"cmd"`
	Repo     string            `json:"repo,omitempty"`
	Settings map[string]string `json:"settings,omitempty"`
	Force    bool              `json:"force,omitempty"`
	Since    uint64            `json:"since,omitempty"`
	Limit    int               `json:"limit,omitempty"`
}

type SocketCommand = Request

type Response struct {
	ID     string      `json:"id,omitempty"`
	OK     bool        `json:"ok"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
	Code   string      `json:"code,omitempty"`
}

const (
	FrameHello = "hello"
	FrameState = "state"
)

type Hello struct {
	Type        string      `json:"type"`
	Protocol    int         `json:"protocol"`
	Version     string      `json:"version"`
	Repos       []RepoState `json:"repos"`
	LastEventID uint64      `json:"lastEventId"`
}

type StateFrame struct {
	Type string    `json:"type"`
	Repo string    `json:"repo"`
	Ts   int64     `json:"ts"`
	Data RepoState `json:"data"`
}

type StatusResult struct {
	Protocol    int         `json:"protocol"`
	Version     string      `json:"version"`
	Daemon      string      `json:"daemon"`
	Repos       []RepoState `json:"repos"`
	LastEventID uint64      `json:"lastEventId"`
}

type VersionResult struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
}

type EventsResult struct {
	Events      []Event `json:"events"`
	LastEventID uint64  `json:"lastEventId"`
}

type CommandResult struct {
	Status string `json:"status"`
	Repo   string `json:"repo,omitempty"`
}

func OKResponse(result interface{}) Response {
	return Response{OK: true, Result: result}
}

func ErrResponse(code, msg string) Response {
	return Response{OK: false, Code: code, Error: msg}
}

func StateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "git-auto-sync")
}

func EnsureStateDir() (string, error) {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return dir, err
	}
	return dir, os.Chmod(dir, 0700)
}

func EventsPath() string {
	return filepath.Join(StateDir(), "events.jsonl")
}

func LogPath() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "git-auto-sync", "daemon.log")
	case "windows":
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = home
		}
		return filepath.Join(dir, "git-auto-sync", "daemon.log")
	default:
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			state = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(state, "git-auto-sync", "daemon.log")
	}
}

type CodedError struct {
	Code string
	Msg  string
}

func (e *CodedError) Error() string { return e.Msg }

func NewCodedError(code, msg string) error {
	return &CodedError{Code: code, Msg: msg}
}

func ErrorCode(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}
