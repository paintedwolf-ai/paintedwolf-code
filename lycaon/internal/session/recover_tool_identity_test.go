package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInterruptedToolProjectionRetainsIdentityAcrossRecoveryPaths(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		name := "startup"
		if scoped {
			name = "session stop"
		}
		t.Run(name, func(t *testing.T) {
			database := testdbfixture.Open(t, "interrupted-identity.db")
			testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
			st := store.NewSQL(database)
			manager := NewManager(st, nil, nil, settings.DefaultSessionLimits())
			recorder := invocation.NewSQLRecorder(database)
			manager.SetInvocationRecorder(recorder)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create interrupted session", err)
			for _, tool := range []string{"read", "command", "http_request"} {
				seedInterruptedTool(t, st, recorder, sess.ID, tool)
			}
			_, err = recorder.InterruptRunning(t.Context())
			testutil.FailErr(t, "interrupt tool owners", err)
			for range 2 {
				if scoped {
					err = manager.recoverInterruptedToolResultPagesForSession(t.Context(), recorder, sess.ID)
				} else {
					err = manager.RecoverInterruptedToolResults(t.Context())
				}
				testutil.FailErr(t, "recover tool projections", err)
			}
			messages, err := st.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read recovered transcript", err)
			assertRecoveredToolIdentities(t, messages)
		})
	}
}

func seedInterruptedTool(t *testing.T, st *store.SQL, recorder *invocation.SQLRecorder, sessionID, tool string) {
	t.Helper()
	contract, ok := toolcontract.Lookup(tool)
	if !ok {
		t.Fatalf("missing tool contract %s", tool)
	}
	testutil.FailErr(t, "append tool request", st.AppendMessages(t.Context(), sessionID, api.Message{
		ID: "assistant-" + tool, Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{ID: "call-" + tool, Name: tool}},
	}))
	_, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: sessionID,
		AssistantMessageID: "assistant-" + tool, ToolCallID: "call-" + tool,
		ToolName: tool, Contract: contract, Args: map[string]any{},
	})
	testutil.FailErr(t, "begin tool receipt", err)
}

func assertRecoveredToolIdentities(t *testing.T, messages []api.Message) {
	t.Helper()
	counts := make(map[string]int)
	for _, message := range messages {
		result := message.ToolResult
		if result == nil {
			continue
		}
		counts[result.Tool]++
		if result.AssistantMessageID != "assistant-"+result.Tool || result.ToolCallID != "call-"+result.Tool {
			t.Errorf("recovered result cannot join its call: %+v", result)
		}
		if result.Invocation == nil || result.Invocation.Status != api.InvocationStatusInterrupted {
			t.Errorf("recovered result lacks interrupted owner evidence: %+v", result)
		}
	}
	for _, tool := range []string{"read", "command", "http_request"} {
		if counts[tool] != 1 {
			t.Errorf("%s result count = %d, want one after replay", tool, counts[tool])
		}
	}
}
