package actionoutcome

import (
	"cmp"
	"slices"
	"sync"
	"time"

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
	session := s.byID[sessionID]
	if session == nil {
		return
	}
	now := time.Now().UTC()
	session.Wakes.Retire(func(r WakeRecord) WakeRecord {
		r.State = api.WakeStateDropped
		r.Reason = api.WakeReasonSessionClosed
		r.Steps = append(slices.Clip(r.Steps), WakeStep{r.State, r.Reason})
		return r
	})
	session.Turns.Retire(func(r TurnRecord) TurnRecord {
		r.Outcome = api.TurnOutcomeKindCanceled
		r.CanceledBy = api.TurnCancellationSourceShutdown
		r.SettledAt = now
		return r
	})
	session.Actions.Retire(func(r api.ActionContinuation) api.ActionContinuation {
		r.State = api.ContinuationStateNotContinued
		r.Reason = api.ContinuationReasonSessionClosed
		r.ResolvedAt = &now
		return r
	})
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
