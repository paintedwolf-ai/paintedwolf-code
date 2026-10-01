package store

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

func reasoningStore(t *testing.T) (*SQL, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "reasoning.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	s := NewSQL(sqlDB)
	sess, err := s.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return s, sess.ID
}

// Subsequent turns reload reasoning from durable history.
func TestModelReasoningRoundTripsThroughTheStore(t *testing.T) {
	ctx := context.Background()
	s, sessionID := reasoningStore(t)

	block := json.RawMessage(`{"type":"reasoning.text","text":"write it","index":0,"signature":"sig-1"}`)
	msg := api.Message{
		Role:      api.MessageRoleAssistant,
		Content:   "writing",
		ToolCalls: []api.ToolCall{{ID: "call_1", Name: "write"}},
		ModelReasoning: &api.ModelReasoning{
			ProviderID: "openrouter-1",
			Model:      "moonshotai/kimi-k2.7-code",
			Text:       "write it",
			Details:    []json.RawMessage{block},
		},
	}
	testutil.FailErr(t, "append message", s.AppendMessages(ctx, sessionID, msg))

	got, err := s.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if len(got) != 1 {
		t.Fatalf("messages = %d want 1", len(got))
	}
	stored := got[0].ModelReasoning
	if stored == nil {
		t.Fatal("reasoning did not survive the store")
	}
	if stored.ProviderID != "openrouter-1" || stored.Model != "moonshotai/kimi-k2.7-code" {
		t.Fatalf("provenance = %+v — without it the trace can never be replayed", stored)
	}
	if stored.Text != "write it" {
		t.Fatalf("text = %q", stored.Text)
	}
	// Preserve signed block bytes exactly.
	if len(stored.Details) != 1 || string(stored.Details[0]) != string(block) {
		t.Fatalf("details = %s want the original bytes", stored.Details)
	}
}

// TestModelReasoningFollowsThePatchedRow keeps trace state row-local.
func TestModelReasoningFollowsThePatchedRow(t *testing.T) {
	ctx := context.Background()
	s, sessionID := reasoningStore(t)

	msg := api.Message{Role: api.MessageRoleAssistant, Content: "thinking"}
	testutil.FailErr(t, "append message", s.AppendMessages(ctx, sessionID, msg))
	got, err := s.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if got[0].ModelReasoning != nil {
		t.Fatalf("streaming row started with a trace: %+v", got[0].ModelReasoning)
	}

	settled := got[0]
	settled.Content = "done"
	settled.ModelReasoning = &api.ModelReasoning{ProviderID: "p", Model: "m", Text: "trace"}
	_, err = s.UpdateMessage(ctx, sessionID, settled.ID, settled)
	testutil.FailErr(t, "settle row", err)
	got, err = s.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get settled messages", err)
	if got[0].ModelReasoning == nil || got[0].ModelReasoning.Text != "trace" {
		t.Fatalf("settled trace = %+v", got[0].ModelReasoning)
	}

	cleared := got[0]
	cleared.ModelReasoning = nil
	_, err = s.UpdateMessage(ctx, sessionID, cleared.ID, cleared)
	testutil.FailErr(t, "clear trace", err)
	got, err = s.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get cleared messages", err)
	if got[0].ModelReasoning != nil {
		t.Fatalf("cleared trace came back: %+v", got[0].ModelReasoning)
	}
}

// TestModelReasoningStaysOffTheWire keeps reasoning internal.
func TestModelReasoningStaysOffTheWire(t *testing.T) {
	raw, err := json.Marshal(api.Message{
		Role:           api.MessageRoleAssistant,
		Content:        "done",
		ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "m", Text: "secret sauce"},
	})
	testutil.FailErr(t, "marshal message", err)
	for _, needle := range []string{"secret sauce", "model_reasoning", "reasoning"} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("wire message leaked %q: %s", needle, raw)
		}
	}
}
