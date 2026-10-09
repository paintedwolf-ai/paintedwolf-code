package promptadmin

import (
	"context"
	"errors"
	"fmt"
	workflowrunstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func testUserNoticeCatalog(t *testing.T) *usernotice.Catalog {
	t.Helper()
	cfg, err := usernotice.LoadNoticeDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	if err != nil {
		t.Fatalf("load user notices: %v", err)
	}
	return usernotice.NewCatalog(cfg)
}

func TestPromptHostErrorCode(t *testing.T) {
	t.Run("provider not configured", func(t *testing.T) {
		err := &failure.ProviderNotConfiguredError{ProviderID: "openai"}
		code := promptHostErrorCode(err)
		if code != "provider_not_configured" {
			t.Fatalf("code = %q", code)
		}
	})

	t.Run("generic", func(t *testing.T) {
		code := promptHostErrorCode(errors.New("boom"))
		if code != "prompt_failed" {
			t.Fatalf("code = %q", code)
		}
	})

	t.Run("distinct error mappings", func(t *testing.T) {
		cases := []struct {
			err  error
			want wire.NoticeCode
		}{
			{guidance.ErrGroundingEscalated, "grounding_escalated"},
			{project.ErrMutationInProgress, "project_mutation_in_progress"},
			{workflowrunstate.ErrActiveRunExists, "workflow_active"},
			{&spendguard.CeilingReached{CeilingUSD: 5, SpentUSD: 5.1}, "session_spend_ceiling_reached"},
			{&workflowrunstate.NotRunnableError{Reason: "paused"}, "workflow_not_runnable"},
			{&failure.ProviderNotConfiguredError{ProviderID: "x"}, "provider_not_configured"},
			{&failure.ProviderEmptyCompletionError{}, "provider_empty_completion"},
			{&failure.ProviderContextTooSmallError{ProviderID: "desktop", Model: "qwen3.5:27b"}, "provider_context_too_small"},
			{&failure.ProviderRateLimitedError{ProviderID: "fireworks", Status: 429}, "provider_rate_limited"},
			{&failure.ProviderOverloadedError{ProviderID: "together-ai-1", Status: 503}, "provider_overloaded"},
			{&failure.ProviderServerError{ProviderID: "together-ai-1", Status: 500}, "provider_server_error"},
			{&providerretry.ProviderRequestRejectedError{ProviderID: "together-ai-1", Status: 400}, "provider_request_rejected"},
			// The batch joins a host fault with its cleanup errors.
			{errors.Join(&promptloop.HostFaultError{Tool: "write", Cause: errors.New("refused")}, errors.New("cleanup")), "host_fault"},
			{errors.New("anything"), "prompt_failed"},
		}
		for _, tc := range cases {
			got := promptHostErrorCode(tc.err)
			if got != tc.want {
				t.Fatalf("promptHostErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
			}
		}
	})
}

func TestPromptHostErrorMapsTransportFailure(t *testing.T) {
	transport := &url.Error{
		Op:  "Post",
		URL: "https://api.example.invalid/v1/messages",
		Err: syscall.ECONNREFUSED,
	}

	cases := []struct {
		name string
		err  error
		want wire.NoticeCode
	}{
		{"refused connection", transport, "provider_unreachable"},
		{"wrapped by the turn", fmt.Errorf("stream turn: %w", transport), "provider_unreachable"},
		{"bound elapsed", fmt.Errorf("discover: %w", context.DeadlineExceeded), "provider_unreachable"},
		{"rate limited", &failure.ProviderRateLimitedError{ProviderID: "fireworks", Status: 429}, "provider_rate_limited"},
		{"overloaded", &failure.ProviderOverloadedError{ProviderID: "together-ai-1", Status: 503}, "provider_overloaded"},
		{"server error", &failure.ProviderServerError{ProviderID: "together-ai-1", Status: 500}, "provider_server_error"},
		{"request rejected", &providerretry.ProviderRequestRejectedError{ProviderID: "together-ai-1", Status: 400}, "provider_request_rejected"},
		{"empty completion", &failure.ProviderEmptyCompletionError{}, "provider_empty_completion"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptHostErrorCode(tc.err); got != tc.want {
				t.Fatalf("promptHostErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRenderPromptHostErrorRendersUnreachableCopy(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	host := renderPromptHostError(catalog, &url.Error{
		Op: "Post", URL: "https://api.example.invalid/v1/messages", Err: syscall.ECONNREFUSED,
	}, false)
	if host.Code != "provider_unreachable" {
		t.Fatalf("code = %q", host.Code)
	}
	if strings.TrimSpace(host.Title) == "" || strings.TrimSpace(host.Message) == "" {
		t.Fatalf("provider_unreachable rendered empty copy: %#v", host)
	}
	if len(host.Actions) == 0 || host.Actions[0] != wire.NoticeActionPromptRetry {
		t.Fatalf("expected prompt_retry action without progress, got %#v", host.Actions)
	}
}

func TestRenderPromptHostErrorIncludesRenderedCopy(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	host := renderPromptHostError(catalog, &failure.ProviderNotConfiguredError{ProviderID: "fireworks"}, false)
	if host.Code != "provider_not_configured" {
		t.Fatalf("code = %q", host.Code)
	}
	if host.Title == "" || host.Message == "" {
		t.Fatalf("expected rendered title/message, got %#v", host)
	}
	if !strings.Contains(host.Message, "fireworks") || !strings.Contains(host.Message, "Settings") {
		t.Fatalf("message = %q", host.Message)
	}
	if len(host.Actions) < 2 || host.Actions[0] != wire.NoticeActionPromptRetry || host.Actions[1] != wire.NoticeActionOpenAiProviders {
		t.Fatalf("expected prompt_retry and open_ai_providers without progress, got %#v", host.Actions)
	}
}

func TestRenderPromptHostErrorIncludesGenericDetail(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	host := renderPromptHostError(catalog, errors.New("dial tcp 127.0.0.1:11434: connection refused"), false)
	if host.Code != "prompt_failed" {
		t.Fatalf("code = %q", host.Code)
	}
	if !strings.Contains(host.Message, "connection refused") {
		t.Fatalf("message missing detail: %q", host.Message)
	}
	if !strings.Contains(host.Title, "Couldn't finish") {
		t.Fatalf("title = %q", host.Title)
	}
	if len(host.Actions) == 0 || host.Actions[0] != wire.NoticeActionPromptRetry {
		t.Fatalf("expected prompt_retry, got %#v", host.Actions)
	}
}

func TestRenderPromptHostErrorHidesProviderServerDetail(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	host := renderPromptHostError(catalog, &failure.ProviderServerError{
		ProviderID: "together-ai-1",
		Model:      "zai-org/GLM-5.3-Flash",
		Status:     500,
		Attempts:   4,
		Detail:     "EngineCore encountered an issue. See stack trace above.",
	}, false)
	if host.Code != "provider_server_error" {
		t.Fatalf("code = %q", host.Code)
	}
	if !strings.Contains(host.Message, "together-ai-1") || !strings.Contains(host.Message, "HTTP 500") ||
		!strings.Contains(host.Message, "tried 4 times") {
		t.Fatalf("message missing structured facts: %q", host.Message)
	}
	if !strings.Contains(host.Title, "model provider") || !strings.Contains(host.Message, "provider's side") ||
		!strings.Contains(host.Message, "rather than with your prompt or local setup") {
		t.Fatalf("message does not identify the likely fault boundary: %#v", host)
	}
	if strings.Contains(host.Message, "EngineCore") || strings.Contains(host.Message, "stack trace") {
		t.Fatalf("message leaked upstream prose: %q", host.Message)
	}
	if len(host.Actions) < 2 || host.Actions[0] != wire.NoticeActionPromptRetry || host.Actions[1] != wire.NoticeActionOpenAiProviders {
		t.Fatalf("expected [prompt_retry, open_ai_providers], got %#v", host.Actions)
	}
}

func TestRenderPromptHostErrorWithProgress(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	host := renderPromptHostError(catalog, &failure.ProviderServerError{
		ProviderID: "together-ai-1",
		Model:      "zai-org/GLM-5.3-Flash",
		Status:     500,
		Attempts:   4,
	}, true)
	if len(host.Actions) < 2 {
		t.Fatalf("expected at least 2 actions with progress, got %#v", host.Actions)
	}
	if host.Actions[0] != wire.NoticeActionPromptKeepGoing || host.Actions[1] != wire.NoticeActionPromptRewindAndRetry {
		t.Fatalf("expected [prompt_keep_going, prompt_rewind_and_retry...], got %#v", host.Actions)
	}
	if len(host.Actions) >= 3 && host.Actions[2] != wire.NoticeActionOpenAiProviders {
		t.Fatalf("expected 3rd action to be open_ai_providers, got %#v", host.Actions)
	}
}

func TestInformationalPromptNoticesOfferNoPromptAction(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	for _, tc := range []struct {
		err  error
		want []wire.NoticeAction
	}{
		{ErrUserImageNotVisible, []wire.NoticeAction{wire.NoticeActionOpenAiProviders}},
		{ErrAttachmentScannedNoText, nil},
		{guidance.ErrGroundingEscalated, nil},
	} {
		for _, progress := range []bool{false, true} {
			host := renderPromptHostError(catalog, tc.err, progress)
			if fmt.Sprint(host.Actions) != fmt.Sprint(tc.want) {
				t.Fatalf("%s actions (progress=%v) = %v, want %v", host.Code, progress, host.Actions, tc.want)
			}
		}
	}
}

func TestPromptActionsStayOffOtherSurfaces(t *testing.T) {
	catalog := testUserNoticeCatalog(t)
	copy := catalog.RenderWire(string(wire.NoticeCodeProviderNotConfigured), nil)
	if fmt.Sprint(copy.Actions) != fmt.Sprint([]string{string(wire.NoticeActionOpenAiProviders)}) {
		t.Fatalf("HTTP rendering carried prompt actions: %v", copy.Actions)
	}
}

type unreadableTranscript struct{ session.Store }

func (unreadableTranscript) GetMessages(context.Context, string) ([]wire.Message, error) {
	return nil, errors.New("transcript read failed")
}

type readableTranscript struct {
	session.Store
	msgs []wire.Message
}

func (r readableTranscript) GetMessages(context.Context, string) ([]wire.Message, error) {
	return r.msgs, nil
}

func TestFailedTurnProgressTreatsUnreadableTranscriptAsProgress(t *testing.T) {
	if !failedTurnProgress(t.Context(), unreadableTranscript{}, "s1") {
		t.Fatal("unknown progress offered a plain retry")
	}
	asked := []wire.Message{{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Content: "go"}}
	if failedTurnProgress(t.Context(), readableTranscript{msgs: asked}, "s1") {
		t.Fatal("a transcript without output reported progress")
	}
	host := renderPromptHostError(testUserNoticeCatalog(t), errors.New("boom"), failedTurnProgress(t.Context(), unreadableTranscript{}, "s1"))
	if len(host.Actions) != 2 || host.Actions[1] != wire.NoticeActionPromptRewindAndRetry {
		t.Fatalf("unknown progress actions = %v", host.Actions)
	}
}

func TestSessionMessagesHaveTurnProgress(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if sessionMessagesHaveTurnProgress(nil) {
			t.Fatal("expected false for nil messages")
		}
	})

	t.Run("only user prompt", func(t *testing.T) {
		msgs := []wire.Message{
			{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Content: "Hello"},
		}
		if sessionMessagesHaveTurnProgress(msgs) {
			t.Fatal("expected false when no progress after user prompt")
		}
	})

	t.Run("assistant replied after user prompt", func(t *testing.T) {
		msgs := []wire.Message{
			{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Content: "Do something"},
			{Role: wire.MessageRoleAssistant, Content: "Working on it..."},
		}
		if !sessionMessagesHaveTurnProgress(msgs) {
			t.Fatal("expected true when assistant replied")
		}
	})

	t.Run("tool call executed after user prompt", func(t *testing.T) {
		msgs := []wire.Message{
			{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Content: "Do something"},
			{Role: wire.MessageRoleTool, Content: "file contents"},
		}
		if !sessionMessagesHaveTurnProgress(msgs) {
			t.Fatal("expected true when tool executed")
		}
	})

	t.Run("multiple turns uses last user ask", func(t *testing.T) {
		msgs := []wire.Message{
			{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Content: "First ask"},
			{Role: wire.MessageRoleAssistant, Content: "Done first ask"},
			{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Content: "Second ask"},
		}
		if sessionMessagesHaveTurnProgress(msgs) {
			t.Fatal("expected false when no progress after second ask")
		}

		msgs = append(msgs, wire.Message{Role: wire.MessageRoleAssistant, Content: "Working on second..."})
		if !sessionMessagesHaveTurnProgress(msgs) {
			t.Fatal("expected true when progress after second ask")
		}
	})
}

func TestShouldPublishPromptHostError(t *testing.T) {
	if shouldPublishPromptHostError(lifecycle.ErrStopping) {
		t.Fatal("user abort must not publish host_error")
	}
	if !shouldPublishPromptHostError(errors.New("boom")) {
		t.Fatal("unexpected failures must publish host_error")
	}
}
