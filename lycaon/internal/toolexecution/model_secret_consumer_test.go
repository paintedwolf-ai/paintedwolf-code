package toolexecution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const modelConsumerGitHubToken = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"

func modelConsumerMatcher(t *testing.T) *secretmatch.Matcher {
	t.Helper()
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("m", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	m.SetFingerprinter(fingerprinter)
	return m
}

func modelConsumerRequest() modelcall.CompletionRequest {
	return modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleTool,
			Content: "1|public\n2|token=" + modelConsumerGitHubToken,
			ToolResult: &api.ToolResult{
				Tool:       "read",
				ToolCallID: "call_read_1",
				ToolArgs:   map[string]any{"path": ".env.local"},
			},
		}},
		Tools: []tools.ToolMeta{{
			Name: "write", Description: "Write a file", ArgsSchema: map[string]any{"type": "object"},
		}},
		Debug: modelcall.RequestDebug{SessionID: "sess-1", ProjectID: "proj-1"},
	}
}

func TestModelSecretScreenRelaysCompletedPublicFetchWithoutApproval(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	screen := llm.NewModelSecretScreen(modelConsumerMatcher(t), exec.Secrets.AskSecretScreen)
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleTool,
			Content: "docs example\nPASSWORD=" + modelConsumerGitHubToken,
			ToolResult: &api.ToolResult{
				Tool: "fetch_url", Outcome: api.ToolResultOutcomeCompleted,
				ToolArgs: map[string]any{"url": "https://docs.example/install"},
			},
		}},
	}
	got, err := screen.Screen(context.Background(), llm.ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "relay public fetch result", err)
	if got.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("public result changed: %q", got.Messages[0].Content)
	}
}

// Trusted destinations bypass the approval card.
func TestModelSecretScreenTrustedDestinationSendsUnchangedWithoutACard(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	screen := llm.NewModelSecretScreen(modelConsumerMatcher(t), exec.Secrets.AskSecretScreen)
	req := modelConsumerRequest()

	got, err := screen.Screen(context.Background(), llm.ScreenDestination{ID: "ollama-1", Trusted: true}, req)
	testutil.FailErr(t, "screen trusted destination", err)
	if got.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("trusted send changed the request: %q", got.Messages[0].Content)
	}

	_, err = screen.Screen(context.Background(), llm.ScreenDestination{ID: "ollama-1"}, req)
	var fault *llm.ModelRequestSecretScreenFaultError
	if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
		t.Fatalf("untrusted destination err = %v, want the card path to be reached", err)
	}
}

// Trust does not disclose protected managed values.
func TestModelSecretScreenTrustedDestinationStillWithholdsNonDisclosableValues(t *testing.T) {
	m := modelConsumerMatcher(t)
	const generated = "host-generated-value-1234567890"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name: "deployment token", Secret: generated, RuleID: secretmatch.ManagedRuleID,
			Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
			Reference: reference, NonDisclosable: true,
		}}
	})
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	screen := llm.NewModelSecretScreen(m, exec.Secrets.AskSecretScreen)
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleTool,
			Content: "token=" + generated + "\nalso=" + modelConsumerGitHubToken,
			ToolResult: &api.ToolResult{
				Tool: "read", ToolCallID: "call_read_1", ToolArgs: map[string]any{"path": ".env"},
			},
		}},
		Debug: modelcall.RequestDebug{SessionID: "root-1", RootSessionID: "root-1"},
	}
	got, err := screen.Screen(context.Background(), llm.ScreenDestination{ID: "ollama-1", Trusted: true}, req)
	testutil.FailErr(t, "screen trusted destination with a managed value", err)
	content := got.Messages[0].Content
	if strings.Contains(content, generated) || !strings.Contains(content, reference) {
		t.Fatalf("managed value crossed to a trusted provider: %q", content)
	}
	if !strings.Contains(content, modelConsumerGitHubToken) {
		t.Fatalf("trusted send stripped a detected value it should have sent unchanged: %q", content)
	}
}
