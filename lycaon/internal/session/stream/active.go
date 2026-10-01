package stream

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

type activeStreamState struct {
	mu        sync.RWMutex
	bySession map[string]string // sessionID -> streaming message id
	tokens    map[string]int    // sessionID -> generating-token estimate
}

func (s *activeStreamState) set(sessionID, messageID string, generatingTokens int) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	if sessionID == "" || messageID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bySession == nil {
		s.bySession = make(map[string]string)
	}
	if s.tokens == nil {
		s.tokens = make(map[string]int)
	}
	s.bySession[sessionID] = messageID
	s.tokens[sessionID] = generatingTokens
}

func (s *activeStreamState) take(sessionID string) string {
	if s == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID := s.bySession[sessionID]
	delete(s.bySession, sessionID)
	delete(s.tokens, sessionID)
	return messageID
}

func (s *activeStreamState) messageID(sessionID string) string {
	if s == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bySession[sessionID]
}

func (s *activeStreamState) generatingTokens(sessionID string) int {
	if s == nil {
		return 0
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokens[sessionID]
}

func (m *State) SetActive(sessionID, messageID string, generatingTokens int) {
	m.activeStream.set(sessionID, messageID, generatingTokens)
}

func (m *State) Finish(ctx context.Context, sessionID string) {
	// Flush the final snapshot before ending the live stream.
	m.Flush(context.WithoutCancel(ctx), sessionID)
	messageID := m.activeStream.take(sessionID)
	m.signalDone(sessionID, messageID)
}

func (m *State) ActiveMessageID(sessionID string) string {
	return m.activeStream.messageID(sessionID)
}

func (m *State) StampMessages(sessionID string, msgs []api.Message) []api.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]api.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = m.StampMessage(sessionID, msg)
	}
	return out
}

func (m *State) StampMessage(sessionID string, msg api.Message) api.Message {
	activeID := m.ActiveMessageID(sessionID)
	if activeID != "" && msg.ID == activeID {
		msg.Status = api.MessageLiveStatusStreaming
		msg.GeneratingTokens = m.activeStream.generatingTokens(sessionID)
	} else {
		msg.Status = api.MessageLiveStatusComplete
	}
	return msg
}
