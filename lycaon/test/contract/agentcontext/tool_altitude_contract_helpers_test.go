package contract

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubCuratorProvider struct {
	id        string
	responses []string
	calls     int
	err       error
}

func (s *stubCuratorProvider) ID() string { return s.id }

func (s *stubCuratorProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if s.err != nil {
		return nil, s.err
	}
	i := s.calls
	s.calls++
	if len(s.responses) == 0 {
		return &modelcall.Completion{Content: `{}`}, nil
	}
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	return &modelcall.Completion{Content: s.responses[i]}, nil
}

func (s *stubCuratorProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		c, err := s.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: c.Content, Done: true}
	}()
	return ch, nil
}

func (s *stubCuratorProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "lite"}}
}

func (s *stubCuratorProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func snapshotFromReadLine(t *testing.T, path, lineContent string, line int) evidence.Ledger {
	t.Helper()
	readJSON := fmt.Sprintf(`{"path":%q,"content":%q,"offset":%d,"end_line":%d,"limit":1}`,
		path, lineContent, line, line)
	return ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": path, "offset": line, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
}
