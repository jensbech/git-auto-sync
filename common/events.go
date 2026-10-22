package common

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	EventCommit    = "commit"
	EventPull      = "pull"
	EventPush      = "push"
	EventSynced    = "synced"
	EventError     = "error"
	EventConflict  = "conflict"
	EventPreflight = "preflight"
	EventOffline   = "offline"
	EventRecovered = "recovered"
	EventPaused    = "paused"
	EventResumed   = "resumed"
)

const (
	CodeNetwork       = "network"
	CodeAuth          = "auth"
	CodeTimeout       = "timeout"
	CodeConflict      = "conflict"
	CodeLock          = "lock"
	CodeXcodeLicense  = "xcode_license"
	CodeGitMissing    = "git_missing"
	CodeNoAuthor      = "no_author"
	CodePushRejected  = "push_rejected"
	CodeConfig        = "config"
	CodeUnknown       = "unknown"
	CodeNotARepo      = "not_a_repo"
	CodeNotWatching   = "not_watching"
	CodeMissingRepo   = "missing_repo"
	CodeBadRequest    = "bad_request"
	CodeUnknownCmd    = "unknown_command"
	CodeAlreadyExists = "already_exists"
	CodeNotFound      = "not_found"
)

type Event struct {
	ID      uint64 `json:"id"`
	Type    string `json:"type"`
	Repo    string `json:"repo"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status,omitempty"`
	Code    string `json:"code,omitempty"`
	Ts      int64  `json:"ts"`
}

type EventEmitter interface {
	Emit(Event)
}

type LogEmitter struct{}

func NewLogEmitter() *LogEmitter {
	return &LogEmitter{}
}

func (e *LogEmitter) Emit(ev Event) {
	if ev.Message != "" {
		log.Printf("event: type=%s repo=%s msg=%s", ev.Type, ev.Repo, ev.Message)
	} else {
		log.Printf("event: type=%s repo=%s status=%s", ev.Type, ev.Repo, ev.Status)
	}
}

type MultiEmitter struct {
	emitters []EventEmitter
}

func NewMultiEmitter(emitters ...EventEmitter) *MultiEmitter {
	return &MultiEmitter{emitters: emitters}
}

func (m *MultiEmitter) Emit(ev Event) {
	for _, e := range m.emitters {
		e.Emit(ev)
	}
}

func NewCodedEvent(eventType, repo, message, code string) Event {
	ev := NewEvent(eventType, repo, message, "")
	ev.Code = code
	return ev
}

func NewEvent(eventType, repo, message, status string) Event {
	return Event{
		Type:    eventType,
		Repo:    repo,
		Message: message,
		Status:  status,
		Ts:      time.Now().Unix(),
	}
}

const EventLogCapacity = 1000

type EventLog struct {
	mu       sync.Mutex
	path     string
	capacity int
	buf      []Event
	start    int
	count    int
	lastID   uint64
	file     *os.File
	lines    int
}

func NewMemoryEventLog(capacity int) *EventLog {
	if capacity <= 0 {
		capacity = EventLogCapacity
	}
	return &EventLog{capacity: capacity, buf: make([]Event, capacity)}
}

func NewEventLog(path string, capacity int) (*EventLog, error) {
	l := NewMemoryEventLog(capacity)
	if path == "" {
		return l, nil
	}
	l.path = path
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	if err := l.rewrite(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *EventLog) load() error {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	skipped := 0
	for {
		line, err := ReadLine(r, MaxRequestSize)
		if errors.Is(err, ErrRequestTooLarge) {
			skipped++
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil || ev.ID == 0 || ev.ID <= l.lastID {
			skipped++
			continue
		}
		l.lastID = ev.ID
		l.push(ev)
	}
	if skipped > 0 {
		log.Printf("events: skipped %d unreadable line(s) in %s", skipped, l.path)
	}
	return nil
}

func (l *EventLog) rewrite() error {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".events-*.jsonl")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	for i := 0; i < l.count; i++ {
		data, err := json.Marshal(l.at(i))
		if err != nil {
			continue
		}
		_, _ = w.Write(append(data, '\n'))
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}

	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	l.file = f
	l.lines = l.count
	return nil
}

func (l *EventLog) push(ev Event) {
	if l.count < l.capacity {
		l.buf[(l.start+l.count)%l.capacity] = ev
		l.count++
		return
	}
	l.buf[l.start] = ev
	l.start = (l.start + 1) % l.capacity
}

func (l *EventLog) at(i int) Event {
	return l.buf[(l.start+i)%l.capacity]
}

func (l *EventLog) Append(ev Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lastID++
	ev.ID = l.lastID
	if ev.Ts == 0 {
		ev.Ts = time.Now().Unix()
	}
	l.push(ev)

	if l.file != nil {
		if data, err := json.Marshal(ev); err == nil {
			if _, err := l.file.Write(append(data, '\n')); err != nil {
				log.Printf("events: write failed: %v", err)
			}
			l.lines++
		}
		if l.lines > 2*l.capacity {
			if err := l.rewrite(); err != nil {
				log.Printf("events: compaction failed: %v", err)
			}
		}
	}
	return ev
}

func (l *EventLog) LastID() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastID
}

func (l *EventLog) Since(since uint64, repo string, limit int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := []Event{}
	for i := l.count - 1; i >= 0; i-- {
		ev := l.at(i)
		if ev.ID <= since {
			break
		}
		if repo != "" && ev.Repo != repo {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
