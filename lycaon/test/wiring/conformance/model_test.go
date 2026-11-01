package conformance_test

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/commandconformance"
)

// caseModel swaps immutable fixture clients between completed case turns.
type caseModel struct {
	mu     sync.RWMutex
	client *llm.MockProvider
}

func (m *caseModel) use(t *testing.T, c commandconformance.Case) {
	t.Helper()
	args := map[string]any{"command": c.Program}
	if c.CapabilityRequest != "" {
		var request map[string]any
		testutil.FailErr(t, "decode case capability request", json.Unmarshal([]byte(c.CapabilityRequest), &request))
		args["capability_request"] = request
	}
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:      "^" + regexp.QuoteMeta("run case "+c.ID) + "$",
		ToolCalls:    []llm.MockToolCall{{ID: "call-" + c.ID, Name: "command", Args: args}},
		FollowUpText: "Case finished.",
	}}})
	m.mu.Lock()
	m.client = client
	m.mu.Unlock()
}

func (m *caseModel) current() *llm.MockProvider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.client == nil {
		return llm.NewMockProvider(nil)
	}
	return m.client
}

func (m *caseModel) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return m.current().Complete(ctx, req)
}

func (m *caseModel) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return m.current().Stream(ctx, req)
}
