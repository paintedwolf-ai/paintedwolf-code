package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecoverOrphanedTurnsClearsStuckBusy(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	hub := events.NewMemoryHub()
	mgr := NewManager(mem, nil, nil, settings.DefaultSessionLimits())
	mgr.SetEventPublisher(&events.Publisher{Hub: hub})
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "busy", mem.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	t.Cleanup(unsub)

	testutil.FailErr(t, "RecoverOrphanedTurns", mgr.Interruptions.RecoverOrphanedTurns(ctx))
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %s want idle", got.Status)
	}

	// Still busy at boot is a host teardown, not a turn failure.
	if got := awaitIdleDisposition(t, ch); got != api.SessionIdleDispositionInterrupted {
		t.Fatalf("recovered disposition = %q, want interrupted", got)
	}
}

func awaitIdleDisposition(t *testing.T, ch <-chan api.EventEnvelope) api.SessionIdleDisposition {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case envelope, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed before a session idle event")
			}
			raw, err := json.Marshal(envelope.Data)
			testutil.FailErr(t, "marshal envelope data", err)
			var ev api.SessionEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			if ev.Status != api.SessionStatusIdle {
				continue
			}
			return ev.IdleDisposition
		case <-deadline:
			t.Fatal("no session idle event published")
			return ""
		}
	}
}

func TestRecoverInterruptedToolResultsAppendsMissingToolRows(t *testing.T) {
	ctx := context.Background()
	database := testdbfixture.Open(t, "recover-turns.db")

	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sqlStore := store.NewSQL(database)
	mgr := NewManager(sqlStore, nil, nil, settings.DefaultSessionLimits())
	recorder := invocation.NewSQLRecorder(database)
	mgr.SetInvocationRecorder(recorder)

	sess, err := sqlStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	testutil.FailErr(t, "busy", sqlStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract is not declared")
	}
	testutil.FailErr(t, "append draft", sqlStore.AppendMessages(ctx, sess.ID, api.Message{
		ID: "asst-1", Role: api.MessageRoleAssistant, Kind: api.MessageKindDraft,
		Content:   "working",
		ToolCalls: []api.ToolCall{{ID: "call-cmd", Name: "command"}},
	}))
	_, err = recorder.Begin(ctx, invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: sess.ID,
		AssistantMessageID: "asst-1", ToolCallID: "call-cmd", ToolName: "command",
		Args: map[string]any{"command": "sleep 600"}, Contract: contract,
	})
	testutil.FailErr(t, "begin receipt", err)

	if _, err := recorder.InterruptRunning(ctx); err != nil {
		testutil.FailErr(t, "interrupt", err)
	}
	testutil.FailErr(t, "RecoverOrphanedTurns", mgr.Interruptions.RecoverOrphanedTurns(ctx))
	testutil.FailErr(t, "RecoverInterruptedToolResults", mgr.Interruptions.RecoverInterruptedToolResults(ctx))
	testutil.FailErr(t, "RecoverInterruptedToolResults replay", mgr.Interruptions.RecoverInterruptedToolResults(ctx))

	got, err := sqlStore.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %s want idle", got.Status)
	}
	msgs, err := sqlStore.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	found := 0
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if msg.ToolResult.ToolCallID == "call-cmd" && msg.ToolResult.Outcome == api.ToolResultOutcomeError {
			found++
			if msg.ToolResult.AssistantMessageID != "asst-1" {
				t.Fatalf("interrupted result lost its assistant identity: %+v", msg.ToolResult)
			}
			if msg.ToolResult.Invocation == nil || msg.ToolResult.Invocation.Status != api.InvocationStatusInterrupted {
				t.Fatalf("interrupted result lost its settled receipt: %+v", msg.ToolResult)
			}
		}
	}
	if found != 1 {
		t.Fatalf("interrupted tool-result count = %d want 1, messages=%+v", found, msgs)
	}
}
