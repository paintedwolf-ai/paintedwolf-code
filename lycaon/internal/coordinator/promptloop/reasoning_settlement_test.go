package promptloop

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSettlementPreservesReasoningAndPresentationBoundaries(t *testing.T) {
	const private = "Internal working note, not shown to the user.\n⟦D:model:internal_step_note⟧\nClose out."
	for _, tc := range []struct {
		name, content, visible string
		calls                  []api.ToolCall
	}{
		{"final answer", "Cleaned up.", "Cleaned up.", nil},
		{"tool step", "Inspect the container.", "", []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{"path": "Dockerfile"}}}},
		{"literal marker", "The closing marker is </think>.", "The closing marker is </think>.", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			directory := t.TempDir()
			db := testdbfixture.OpenPath(t, filepath.Join(directory, "store.db"))
			testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, directory)
			messages := store.NewSQL(db)
			sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			execution, err := messages.BeginTurn(ctx, store.TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
			testutil.FailErr(t, "begin turn", err)
			message := api.Message{ID: uuid.NewString(), Role: api.MessageRoleAssistant, CreatedAt: time.Now().UTC()}
			testutil.FailErr(t, "append placeholder", messages.AppendMessages(ctx, sess.ID, message))
			var replay string
			loop := NewPromptLoopForTest(PromptLoopDeps{Projection: ProjectionDeps{
				SettleModelOutput: messages.SettleModelOutput,
				UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
					_, err := messages.UpdateMessage(ctx, sessionID, messageID, msg)
					return err
				},
				Streams: &testMessageStreams{cachereplay: func(_ string, content string, _ []string) { replay = content }},
			}})
			completion := &modelcall.Completion{
				ProviderID: "fixture", Model: "fixture-model", Content: tc.content, Reasoning: private,
				ReasoningDetails: []json.RawMessage{json.RawMessage(`{"type":"reasoning.encrypted","data":"opaque"}`)}, ToolCalls: tc.calls,
			}
			state := &promptLoopTurnState{attemptID: execution.Attempt.ID}
			_, err = loop.Projection.settleAssistantStreamTurn(ctx, sess.ID, state, message, completion, nil, uuid.NewString(), nextModelIteration(state), func(text string) string { return text })
			testutil.FailErr(t, "settle output", err)
			pending, err := store.NewSQL(db).ListPendingModelOutputProjections(ctx)
			testutil.FailErr(t, "reload canonical output", err)
			if len(pending) != 1 || pending[0].Output.ID != state.output.id {
				t.Fatalf("canonical output identity changed: %+v", pending)
			}
			output := pending[0].Output
			var reasoning api.ModelReasoning
			testutil.FailErr(t, "decode canonical reasoning", json.Unmarshal([]byte(output.ReasoningJSON), &reasoning))
			if output.Content != tc.content || reasoning.Text != private || len(reasoning.Details) != 1 || reasoning.ProviderID != "fixture" {
				t.Fatalf("canonical channels changed: content=%q reasoning=%+v", output.Content, reasoning)
			}
			reloaded, err := store.NewSQL(db).GetMessage(ctx, sess.ID, message.ID)
			testutil.FailErr(t, "reload transcript", err)
			if reloaded.Content != tc.content || reloaded.ModelReasoning == nil || reloaded.ModelReasoning.Text != private {
				t.Fatalf("reload lost canonical channels: %+v", reloaded)
			}
			if visible := messageview.TranscriptMessage(reloaded); visible.Content != tc.visible || replay != tc.visible {
				t.Fatalf("presentation differs: reload=%q replay=%q want=%q", visible.Content, replay, tc.visible)
			}
		})
	}
}
