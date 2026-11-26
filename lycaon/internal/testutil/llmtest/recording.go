// Package llmtest provides scripted clients and request capture for tests.
package llmtest

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// RecordingClient wraps an LLMClient and captures completion requests for tests.
type RecordingClient struct {
	Inner        modelcall.LLMClient
	changed      chan struct{}
	responseGate chan struct{}
	mu           sync.Mutex
	last         modelcall.CompletionRequest
	all          []modelcall.CompletionRequest
}

// NewRecordingClient wraps inner for test observability.
func NewRecordingClient(inner modelcall.LLMClient) *RecordingClient {
	return &RecordingClient{Inner: inner}
}

// Complete records req and delegates to Inner.
func (c *RecordingClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := c.record(ctx, req); err != nil {
		return nil, err
	}
	if c.Inner == nil {
		return &modelcall.Completion{}, nil
	}
	return c.Inner.Complete(ctx, req)
}

// Stream records req and delegates to Inner.
func (c *RecordingClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if err := c.record(ctx, req); err != nil {
		return nil, err
	}
	if c.Inner == nil {
		ch := make(chan modelcall.StreamChunk)
		close(ch)
		return ch, nil
	}
	return c.Inner.Stream(ctx, req)
}

// LastRequest returns the most recent completion request.
func (c *RecordingClient) LastRequest() modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// AllRequests returns every recorded completion request in order.
func (c *RecordingClient) AllRequests() []modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]modelcall.CompletionRequest, len(c.all))
	copy(out, c.all)
	return out
}

func (c *RecordingClient) HoldResponses() func() {
	c.mu.Lock()
	gate := make(chan struct{})
	c.responseGate = gate
	c.mu.Unlock()
	return sync.OnceFunc(func() { close(gate) })
}

func (c *RecordingClient) record(ctx context.Context, req modelcall.CompletionRequest) error {
	c.mu.Lock()
	gate := c.responseGate
	c.last = req
	c.all = append(c.all, req)
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
	}
	return nil
}

func (c *RecordingClient) AwaitRequest(ctx context.Context, index int) (modelcall.CompletionRequest, error) {
	for {
		c.mu.Lock()
		if index < len(c.all) {
			request := c.all[index]
			c.mu.Unlock()
			return request, nil
		}
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return modelcall.CompletionRequest{}, ctx.Err()
		case <-changed:
		}
	}
}
