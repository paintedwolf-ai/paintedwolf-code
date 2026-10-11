package session_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptPageProjectsToolStepWithoutChangingModelHistory(t *testing.T) {
	st := store.NewMemory()
	mgr := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	step := api.Message{ID: "step", Role: api.MessageRoleAssistant, Content: "internal tool explanation",
		ToolCalls: []api.ToolCall{{ID: "call", Name: "wait"}}, Visibility: api.MessageVisibilityTranscript}
	note := api.Message{ID: "note", Role: api.MessageRoleAssistant, Content: "The CLI implementation is ready.", Visibility: api.MessageVisibilityTranscript}
	testutil.FailErr(t, "append step and progress note", st.AppendMessages(t.Context(), sess.ID, step, note))
	page, err := mgr.Runner.Transcript.GetTranscriptPage(t.Context(), sess.ID, api.TranscriptPageQuery{})
	testutil.FailErr(t, "read transcript", err)
	if len(page.Messages) != 2 || page.Messages[0].Content != "" || len(page.Messages[0].ToolCalls) != 1 || page.Messages[1].Content != note.Content {
		t.Fatalf("transcript lost cards or progress note: %+v", page.Messages)
	}
	history, err := mgr.Runner.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read model history", err)
	if len(history) != 2 || history[0].Content != step.Content {
		t.Fatalf("model history changed: %+v", history)
	}
}
