package actionoutcome

import (
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
)

type Sessions struct {
	mu   sync.Mutex
	byID map[string]*Session
}

type Session struct {
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
	session := &Session{Actions: NewHistory[api.ActionContinuation](8), Turns: NewHistory[TurnRecord](32)}
	s.byID[sessionID] = session
	return session
}

func (s *Sessions) Forget(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, sessionID)
}
