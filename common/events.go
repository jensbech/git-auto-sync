package common

import (
	"log"
	"time"
)

type Event struct {
	Type    string `json:"type"`
	Repo    string `json:"repo"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status,omitempty"`
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

func NewEvent(eventType, repo, message, status string) Event {
	return Event{
		Type:    eventType,
		Repo:    repo,
		Message: message,
		Status:  status,
		Ts:      time.Now().Unix(),
	}
}
