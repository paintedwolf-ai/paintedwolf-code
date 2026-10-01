//go:build integration

package property

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// historyWriter records every row exactly as written so the rebuilt history can be
// compared against the same writes without modelling the prompt loop.
type historyWriter struct {
	ctx       context.Context
	store     *store.SQL
	sessionID string
	written   []api.Message
}

func (w *historyWriter) append(t *rapid.T, step string, msgs ...api.Message) {
	failErr(t, step, w.store.AppendMessages(w.ctx, w.sessionID, msgs...))
	w.written = append(w.written, msgs...)
}

func (w *historyWriter) patchDraftStatus(t *rapid.T, step, messageID string, status api.DraftStatus) {
	for i := range w.written {
		if w.written[i].ID != messageID {
			continue
		}
		patch := w.written[i]
		patch.DraftStatus = status
		_, err := w.store.UpdateMessage(w.ctx, w.sessionID, messageID, patch)
		failErr(t, step, err)
		w.written[i] = patch
		return
	}
	t.Fatalf("%s: message %s was never written", step, messageID)
}

type historyAction struct {
	name  string
	write func(*rapid.T, *historyWriter)
}

func (a historyAction) String() string { return a.name }

var promptHistoryActions = []historyAction{
	{name: "user message", write: func(t *rapid.T, w *historyWriter) {
		w.append(t, "append user", api.Message{
			ID:      uuid.NewString(),
			Role:    api.MessageRoleUser,
			Content: rapid.StringMatching(`[a-zA-Z0-9 ]{3,30}`).Draw(t, "user_content"),
		})
	}},
	{name: "committed assistant draft", write: func(t *rapid.T, w *historyWriter) {
		w.append(t, "append assistant", api.Message{
			ID:          uuid.NewString(),
			Role:        api.MessageRoleAssistant,
			Kind:        api.MessageKindDraft,
			DraftStatus: api.DraftStatusCommitted,
			Content:     rapid.StringMatching(`[a-zA-Z0-9 ]{3,30}`).Draw(t, "assistant_content"),
		})
	}},
	{name: "tool call and result", write: func(t *rapid.T, w *historyWriter) {
		callID := uuid.NewString()
		w.append(t, "append tool exchange",
			api.Message{
				ID:        uuid.NewString(),
				Role:      api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{{ID: callID, Name: "command", Args: map[string]any{"argv": []any{"pwd"}}}},
			},
			api.Message{
				ID:         uuid.NewString(),
				Role:       api.MessageRoleTool,
				ToolResult: &api.ToolResult{ToolCallID: callID, Content: "/workspace"},
				Content:    "/workspace",
			},
		)
	}},
	{name: "rejected prose with host nudge", write: func(t *rapid.T, w *historyWriter) {
		attempt := api.Message{
			ID:          uuid.NewString(),
			Role:        api.MessageRoleAssistant,
			Content:     "prose that gets rejected",
			DraftStatus: api.DraftStatusLive,
			Visibility:  api.MessageVisibilityInternal,
		}
		w.append(t, "append provisional", attempt)
		w.patchDraftStatus(t, "patch rejected draft", attempt.ID, api.DraftStatusRejected)
		w.append(t, "append nudge", api.Message{
			ID:         uuid.NewString(),
			Role:       api.MessageRoleUser,
			Content:    "Turn rejected: incomplete check.",
			Visibility: api.MessageVisibilityInternal,
		})
	}},
	{name: "withdrawn draft", write: func(t *rapid.T, w *historyWriter) {
		draft := api.Message{
			ID:          uuid.NewString(),
			Role:        api.MessageRoleAssistant,
			Kind:        api.MessageKindDraft,
			DraftStatus: api.DraftStatusLive,
			Content:     "withdrawn provisional draft",
			Visibility:  api.MessageVisibilityInternal,
		}
		w.append(t, "append draft slot", draft)
		w.patchDraftStatus(t, "patch withdrawn draft", draft.ID, api.DraftStatusWithdrawn)
	}},
	{name: "host steer", write: func(t *rapid.T, w *historyWriter) {
		w.append(t, "append steer", api.Message{
			ID:         uuid.NewString(),
			Role:       api.MessageRoleUser,
			Content:    "host continuation",
			Visibility: api.MessageVisibilityInternal,
		})
	}},
}

// TestPromptHistorySurvivesStoreRoundTrip assembles the rows as written and the rows
// read back from SQL, and requires the same model-facing history after every write.
func TestPromptHistorySurvivesStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "history-parity.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(sqlDB)

	rapid.Check(t, func(t *rapid.T) {
		sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(t, "create session", err)
		writer := &historyWriter{ctx: ctx, store: st, sessionID: sess.ID}
		for step, stepCount := 0, rapid.IntRange(3, 12).Draw(t, "step_count"); step < stepCount; step++ {
			action := rapid.SampledFrom(promptHistoryActions).Draw(t, "action")
			action.write(t, writer)

			written, _ := promptassembly.Assemble(sess, writer.written, promptassembly.Config{})
			stored, err := st.GetMessages(ctx, sess.ID)
			failErr(t, "get messages from db", err)
			rebuilt, _ := promptassembly.Assemble(sess, stored, promptassembly.Config{})

			if len(written) != len(rebuilt) {
				t.Fatalf("history length mismatch at step %d (%s): written=%d, rebuilt=%d", step, action.name, len(written), len(rebuilt))
			}
			for i := range written {
				w, r := written[i], rebuilt[i]
				if w.ID != r.ID || w.Role != r.Role || w.Content != r.Content {
					t.Fatalf("history divergence at step %d[%d] (%s): written={id=%s role=%s content=%q}, rebuilt={id=%s role=%s content=%q}",
						step, i, action.name, w.ID, w.Role, w.Content, r.ID, r.Role, r.Content)
				}
				if !reflect.DeepEqual(w.ToolCalls, r.ToolCalls) {
					t.Fatalf("tool calls divergence at step %d[%d] (%s): written=%+v, rebuilt=%+v", step, i, action.name, w.ToolCalls, r.ToolCalls)
				}
			}
		}
	})
}
