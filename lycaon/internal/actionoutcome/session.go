package actionoutcome

import (
	"cmp"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"sync"
)

type Sessions struct {
	mu   sync.Mutex
	byID map[string]*Session
}

type Session struct {
	Wakes   *History[WakeRecord]
	Actions *History[api.ActionContinuation]
	Turns   *History[TurnRecord]
}

func (s *Sessions) For(sessionID string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = make(map[string]*Session)
	}
	if session := s.byID[sessionID]; session != nil {
		return session
	}
	session := &Session{Wakes: NewHistory[WakeRecord](32), Actions: NewHistory[api.ActionContinuation](8), Turns: NewHistory[TurnRecord](32)}
	s.byID[sessionID] = session
	return session
}

func (s *Sessions) Forget(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, sessionID)
}

type Trail struct {
	Wakes         []WakeRecord             `json:"wakes"`
	Turns         []TurnRecord             `json:"turns"`
	Continuations []api.ActionContinuation `json:"continuations"`
}

func (s *Sessions) Trails() map[string]Trail {
	s.mu.Lock()
	defer s.mu.Unlock()
	trails := make(map[string]Trail, len(s.byID))
	for id, session := range s.byID {
		trace := Trail{Wakes: session.Wakes.Snapshot(), Turns: session.Turns.Snapshot(), Continuations: session.Actions.Snapshot()}
		slices.SortFunc(trace.Wakes, func(a, b WakeRecord) int { return cmp.Compare(a.Sequence, b.Sequence) })
		slices.SortFunc(trace.Turns, func(a, b TurnRecord) int { return a.AdmittedAt.Compare(b.AdmittedAt) })
		slices.SortFunc(trace.Continuations, func(a, b api.ActionContinuation) int { return a.OpenedAt.Compare(b.OpenedAt) })
		trails[id] = trace
	}
	return trails
}
