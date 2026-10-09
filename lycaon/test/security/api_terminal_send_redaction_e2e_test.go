package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestTerminalSendInputRedactedInPersistedTranscript(t *testing.T) {
	const secret = "pty-secret-passphrase-do-not-persist"
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "drive the menu",
		ToolCalls: []llm.MockToolCall{{
			ID:   "ts1",
			Name: "terminal_send",
			Args: map[string]any{"id": "missing-handle", "input": secret},
		}},
		FollowUpText: "drove",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	if err := h.Sessions.Manager.Chats.SetAgentType(ctx, sess.ID, "implementer"); err != nil {
		testutil.FailErr(t, "SetAgentType", err)
	}
	activation := turnload.NewLedger()
	activation.Activate(sess.ID, []string{"terminal_send"}, "type into the terminal")
	h.Sessions.Manager.SetTurnLoads(activation)

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "drive the menu"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	// Durable model history retains the input.
	storeMsgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "Store.GetMessages", err)
	foundStore := false
	for _, m := range storeMsgs {
		if m.Role != wire.MessageRoleAssistant {
			continue
		}
		for _, call := range m.ToolCalls {
			if call.Name != "terminal_send" {
				continue
			}
			foundStore = true
			raw, _ := call.Args["input"].(string)
			if raw != secret {
				t.Fatalf("store terminal_send.input = %q want cleartext for model history", raw)
			}
		}
	}
	if !foundStore {
		t.Fatalf("expected store assistant terminal_send tool call; msgs=%+v", storeMsgs)
	}

	// Transcript projection redacts by tool identity.
	page, err := h.Sessions.Manager.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, wire.TranscriptPageQuery{})
	testutil.FailErr(t, "GetTranscriptPage", err)
	foundTranscript := false
	for _, m := range page.Messages {
		if m.Role != wire.MessageRoleAssistant {
			continue
		}
		for _, call := range m.ToolCalls {
			if call.Name != "terminal_send" {
				continue
			}
			foundTranscript = true
			raw, _ := call.Args["input"].(string)
			if raw == secret || strings.Contains(raw, secret) {
				t.Fatalf("transcript terminal_send.input still cleartext: %q", raw)
			}
			if raw != messageview.RedactedToolArgPlaceholder {
				t.Fatalf("transcript terminal_send.input = %q want %q", raw, messageview.RedactedToolArgPlaceholder)
			}
		}
	}
	if !foundTranscript {
		t.Fatalf("expected transcript terminal_send tool call; msgs=%+v", page.Messages)
	}
}
