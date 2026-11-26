package actionoutcome

import (
	"cmp"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions struct {
	mu       sync.Mutex
	instance string
	byID     map[string]*Session
}

type Session struct {
	instance  string
	defectsMu sync.Mutex
	defects   []WakeDefect
	Notices   Notices
	Wakes     *History[WakeRecord]
	Actions   *History[api.ActionContinuation]
	Turns     *History[TurnRecord]
}

func (s *Sessions) For(sessionID string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.instance = uuid.NewString()
		s.byID = make(map[string]*Session)
	}
	if session := s.byID[sessionID]; session != nil {
		return session
	}
	session := &Session{instance: s.instance, Wakes: NewHistory[WakeRecord](32), Actions: NewHistory[api.ActionContinuation](8), Turns: NewHistory[TurnRecord](32)}
	s.byID[sessionID] = session
	return session
}

func (s *Sessions) Forget(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, sessionID)
}

type Trail struct {
	HostInstance  string                   `json:"host_instance"`
	Defects       []WakeDefect             `json:"defects,omitempty"`
	Wakes         []WakeRecord             `json:"wakes"`
	Turns         []TurnRecord             `json:"turns"`
	Continuations []api.ActionContinuation `json:"continuations"`
}

func (s *Sessions) Trails() map[string]Trail {
	s.mu.Lock()
	defer s.mu.Unlock()
	trails := make(map[string]Trail, len(s.byID))
	for id, session := range s.byID {
		session.defectsMu.Lock()
		defects := slices.Clone(session.defects)
		session.defectsMu.Unlock()
		trace := Trail{HostInstance: session.instance, Defects: defects, Wakes: session.Wakes.Snapshot(), Turns: session.Turns.Snapshot(), Continuations: session.Actions.Snapshot()}
		slices.SortFunc(trace.Wakes, func(a, b WakeRecord) int { return cmp.Compare(a.Sequence, b.Sequence) })
		slices.SortFunc(trace.Turns, func(a, b TurnRecord) int { return a.AdmittedAt.Compare(b.AdmittedAt) })
		slices.SortFunc(trace.Continuations, func(a, b api.ActionContinuation) int { return a.OpenedAt.Compare(b.OpenedAt) })
		trails[id] = trace
	}
	return trails
}
