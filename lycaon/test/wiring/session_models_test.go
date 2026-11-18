package wiring

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// sessionModels answers each session's requests with the model its test
// started that session with, so one host serves many scripted sessions.
type sessionModels struct {
	mu      sync.RWMutex
	clients map[string]modelcall.LLMClient
	// unscripted answers host work that names no session.
	unscripted modelcall.LLMClient
}

func newSessionModels() *sessionModels {
	return &sessionModels{clients: map[string]modelcall.LLMClient{}, unscripted: llm.NewMockProvider(nil)}
}

func (m *sessionModels) use(sessionID string, client modelcall.LLMClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[sessionID] = client
}

// route picks the model by the request's session, then its root and parent,
// so a worker's turn reaches the script of the chat that started it.
func (m *sessionModels) route(req modelcall.CompletionRequest) (modelcall.LLMClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	named := false
	for _, id := range []string{req.Debug.SessionID, req.Debug.RootSessionID, req.Debug.ParentSessionID} {
		if id == "" {
			continue
		}
		named = true
		if client, ok := m.clients[id]; ok {
			return client, nil
		}
	}
	if named {
		return nil, fmt.Errorf("no model script for session %q", req.Debug.SessionID)
	}
	return m.unscripted, nil
}

func (m *sessionModels) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	client, err := m.route(req)
	if err != nil {
		return nil, err
	}
	return client.Complete(ctx, req)
}

func (m *sessionModels) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	client, err := m.route(req)
	if err != nil {
		return nil, err
	}
	return client.Stream(ctx, req)
}
