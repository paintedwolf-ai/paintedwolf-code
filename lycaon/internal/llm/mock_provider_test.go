package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMockProviderTextResponse(t *testing.T) {
	p := NewMockProvider(&MockConfig{
		Responses: []MockResponseEntry{
			{Pattern: ".*", Text: "I understand. How can I help you further?"},
		},
	})

	completion, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	testutil.FailErr(t, "p.Complete failed", err)
	if !strings.Contains(completion.Content, "I understand") {
		t.Fatalf("content = %q", completion.Content)
	}
}

func TestMockProviderToolThenFollowUp(t *testing.T) {
	p := NewMockProvider(&MockConfig{
		Responses: []MockResponseEntry{
			{
				Pattern:      "read.*readme",
				ToolCalls:    []MockToolCall{{ID: "call_1", Name: "read", Args: map[string]any{"path": "README.md"}}},
				FollowUpText: "I've read README.md.",
			},
		},
	})

	first, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read the readme"}},
		Tools:    []tools.ToolMeta{{Name: "read"}},
	})
	testutil.FailErr(t, "p.Complete failed", err)
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "read" {
		t.Fatalf("first completion = %+v", first)
	}

	second, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleUser, Content: "read the readme"},
			{Role: api.MessageRoleAssistant, ToolCalls: first.ToolCalls},
			{Role: api.MessageRoleTool, Content: "file contents"},
		},
		Tools: []tools.ToolMeta{{Name: "read"}},
	})
	testutil.FailErr(t, "p.Complete failed", err)
	if second.Content != "I've read README.md." {
		t.Fatalf("follow-up = %q", second.Content)
	}
}

func TestMockProviderSkipsEntryWhenToolFilteredFromSurface(t *testing.T) {
	p := NewMockProvider(&MockConfig{Responses: []MockResponseEntry{
		{
			Pattern:   "write.*file",
			ToolCalls: []MockToolCall{{ID: "c", Name: "write", Args: map[string]any{"path": "a.txt"}}},
		},
		{Pattern: "write.*file", Text: "write is unavailable"},
	}})
	completion, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:       api.MessageRoleUser,
			Content:    "write a file",
			Visibility: api.MessageVisibilityTranscript,
		}},
		Tools: []tools.ToolMeta{{Name: "read"}, {Name: "task"}},
	})
	testutil.FailErr(t, "p.Complete failed", err)
	if len(completion.ToolCalls) != 0 || completion.Content != "write is unavailable" {
		t.Fatalf("completion = %+v want tool-free fallback", completion)
	}
}

func TestMockProviderSkipsToolEntryOnEmptySurface(t *testing.T) {
	p := NewMockProvider(&MockConfig{Responses: []MockResponseEntry{
		{
			Pattern:   "write.*file",
			ToolCalls: []MockToolCall{{ID: "c", Name: "write", Args: map[string]any{"path": "a.txt"}}},
		},
		{Pattern: "write.*file", Text: "write is unavailable"},
	}})
	completion, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:       api.MessageRoleUser,
			Content:    "write a file",
			Visibility: api.MessageVisibilityTranscript,
		}},
	})
	testutil.FailErr(t, "p.Complete failed", err)
	if len(completion.ToolCalls) != 0 || completion.Content != "write is unavailable" {
		t.Fatalf("completion = %+v want tool-free fallback", completion)
	}
}

func TestMockProviderSkipsTaskEntryWithoutTaskSurface(t *testing.T) {
	p := NewMockProvider(&MockConfig{Responses: []MockResponseEntry{
		{Pattern: "^dispatch", ToolCalls: []MockToolCall{{ID: "t", Name: "task", Args: map[string]any{"agent_type": "implementer"}}}},
		{Pattern: "^dispatch", ToolCalls: []MockToolCall{{ID: "w", Name: "write", Args: map[string]any{"path": "a.txt"}}}},
	}})
	completion, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "dispatch change", Visibility: api.MessageVisibilityTranscript}},
		Tools:    []tools.ToolMeta{{Name: "write"}, {Name: "read"}},
	})
	testutil.FailErr(t, "p.Complete failed", err)
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "write" {
		t.Fatalf("tool calls = %+v want write", completion.ToolCalls)
	}
}

func TestMockProviderCallsVerifyAfterSourceEvidenceHold(t *testing.T) {
	p := NewMockProvider(&MockConfig{
		Responses: []MockResponseEntry{{Pattern: "implement", FollowUpText: "done"}},
	})
	completion, err := p.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleUser, Content: "implement the change"},
			{Role: api.MessageRoleUser, Content: "Code: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nRun verify"},
		},
		Tools: []tools.ToolMeta{{Name: "verify"}},
	})
	testutil.FailErr(t, "Complete", err)
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "verify" {
		t.Fatalf("tool calls = %+v want verify", completion.ToolCalls)
	}
}

func TestMockProviderStream(t *testing.T) {
	p := NewMockProvider(&MockConfig{
		Responses: []MockResponseEntry{{Pattern: ".*", Text: "hello world"}},
	})
	ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "p.Stream failed", err)
	var parts []string
	for chunk := range ch {
		parts = append(parts, chunk.Content)
	}
	if strings.Join(parts, "") != "hello world" {
		t.Fatalf("stream = %q", strings.Join(parts, ""))
	}
}
