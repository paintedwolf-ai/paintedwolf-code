package llm

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// RecordingClient wraps an LLMClient and captures completion requests for tests.
type RecordingClient struct {
	Inner modelcall.LLMClient
	mu    sync.Mutex
	last  modelcall.CompletionRequest
	all   []modelcall.CompletionRequest
}

// NewRecordingClient wraps inner for test observability.
func NewRecordingClient(inner modelcall.LLMClient) *RecordingClient {
	return &RecordingClient{Inner: inner}
}

// Complete records req and delegates to Inner.
func (c *RecordingClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.record(req)
	if c.Inner == nil {
		return &modelcall.Completion{}, nil
	}
	return c.Inner.Complete(ctx, req)
}

// Stream records req and delegates to Inner.
func (c *RecordingClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.record(req)
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

// RequestsWhere preserves request order; a nil predicate matches every request.
func (c *RecordingClient) RequestsWhere(pred func(modelcall.CompletionRequest) bool) []modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []modelcall.CompletionRequest
	for _, req := range c.all {
		if pred == nil || pred(req) {
			out = append(out, req)
		}
	}
	return out
}

func (c *RecordingClient) record(req modelcall.CompletionRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = req
	c.all = append(c.all, req)
}
