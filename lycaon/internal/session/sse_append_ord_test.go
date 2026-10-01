package session

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Every transcript event carries the ordinal minted by its store write.
func TestAppendMessagesSSECarriesOrdForEveryMessageKind(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "sse-ord-kinds.db")

	store := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	const runID = "run-sse-ord-kinds"
	now := time.Now().UTC()
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at
		) VALUES (?, ?, ?, 'plan', '1.0.0', 'running', 'stub', ?, ?)
	`, runID, sess.ID, sess.ProjectID, db.FormatTime(now), db.FormatTime(now))
	testutil.FailErr(t, "seed workflow run", err)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	for _, tc := range appendSSEOrdCases() {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.msg
			msg.ID = uuid.NewString()
			msg.CreatedAt = time.Now().UTC()
			if msg.Kind == wire.MessageKindProgressUpdate || msg.Kind == wire.MessageKindProgressComplete {
				msg.WorkflowRunID = runID
			}
			testutil.FailErr(t, "appendMessages", mgr.appendMessages(ctx, sess.ID, msg))

			ev := waitParentMessageAppend(t, ch, sess.ID, msg.ID)
			if ev.Message.Ord <= 0 {
				t.Fatalf("SSE %s ord = %d want > 0", tc.name, ev.Message.Ord)
			}
			if ev.Message.Seq <= 0 {
				t.Fatalf("SSE %s seq = %d want > 0", tc.name, ev.Message.Seq)
			}
			stored, err := store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "GetMessages", err)
			var row wire.Message
			for _, m := range stored {
				if m.ID == msg.ID {
					row = m
					break
				}
			}
			if row.ID == "" {
				t.Fatalf("store missing id %s", msg.ID)
			}
			if ev.Message.Ord != row.Ord || ev.Message.Seq != row.Seq {
				t.Fatalf("SSE clocks (ord=%d seq=%d) != store (ord=%d seq=%d) for %s",
					ev.Message.Ord, ev.Message.Seq, row.Ord, row.Seq, tc.name)
			}
		})
	}
}

type sseOrdCase struct {
	name string
	msg  wire.Message
}

func appendSSEOrdCases() []sseOrdCase {
	cases := []sseOrdCase{
		{name: "user", msg: wire.Message{Role: wire.MessageRoleUser, Content: "hi", Visibility: wire.MessageVisibilityTranscript}},
		{name: "assistant", msg: wire.Message{Role: wire.MessageRoleAssistant, Content: "hello", Visibility: wire.MessageVisibilityTranscript}},
		{name: "tool", msg: wire.Message{
			Role:       wire.MessageRoleTool,
			Content:    `{"ok":true}`,
			Visibility: wire.MessageVisibilityTranscript,
			ToolResult: &wire.ToolResult{Content: `{"ok":true}`, Outcome: wire.ToolResultOutcomeCompleted},
		}},
	}
	for _, kind := range wire.AllMessageKinds() {
		msg := wire.Message{
			Role:       wire.MessageRoleSystem,
			Kind:       kind,
			Visibility: wire.MessageVisibilityTranscript,
			Content:    string(kind),
		}
		switch kind {
		case wire.MessageKindDraft:
			msg.Role = wire.MessageRoleAssistant
			msg.DraftStatus = wire.DraftStatusCommitted
		case wire.MessageKindBlueprint:
			msg.Role = wire.MessageRoleAssistant
			msg.Blueprint = &wire.BlueprintMeta{
				BlueprintPath: "plan-sse-ord",
				Status:        wire.BlueprintTranscriptStatusProposed,
				Phase:         wire.BlueprintCardPhaseReady,
			}
		case wire.MessageKindProgressUpdate:
			msg.ProgressUpdate = &wire.ProgressUpdateMeta{
				Initial: true,
				Seq:     1,
				Steps:   []wire.ProgressStep{{State: "pending", Label: "a"}},
			}
			msg.Content = ""
		case wire.MessageKindProgressComplete:
			msg.ProgressComplete = &wire.ProgressCompleteMeta{
				Seq:   1,
				Steps: []wire.ProgressStep{{State: "done", Label: "a"}},
			}
			msg.Content = ""
		case wire.MessageKindWorkflowBoundary:
			msg.Visibility = wire.MessageVisibilityInternal
			msg.WorkflowBoundary = &wire.WorkflowBoundaryMeta{
				Event:      string(wire.WorkflowBoundaryKindStarted),
				WorkflowID: "plan",
			}
		case wire.MessageKindWorkflowFeedback:
			msg.WorkflowFeedback = &wire.WorkflowFeedbackMeta{
				Prompt:       "ok?",
				ResponseType: wire.FeedbackResponseText,
			}
		case wire.MessageKindIndexWarming:
			msg.Content = "warmed"
			msg.IndexWarming = &wire.IndexWarmingMeta{Trigger: "test"}
		case wire.MessageKindWorkflowExplain:
			msg.WorkflowExplain = &wire.WorkflowExplainMeta{PhaseID: "ingest", Summary: "A scan runs first", Body: "Why."}
		case wire.MessageKindSuperseded:
			msg.Role = wire.MessageRoleAssistant
		case wire.MessageKindAgentNote:
			msg.Role = wire.MessageRoleAssistant
			msg.Grounding = &wire.CitationGrounding{Traced: true}
		case wire.MessageKindCompletionReport, wire.MessageKindIterationCapCloseout, wire.MessageKindHostKick,
			wire.MessageKindCoordinatorGuidance, wire.MessageKindHostNudge, wire.MessageKindHostLoopWake,
			wire.MessageKindHostSecretRedactionNotice, wire.MessageKindUserContinuation:
		}
		cases = append(cases, sseOrdCase{name: "kind_" + string(kind), msg: msg})
	}
	return cases
}

func waitParentMessageAppend(t *testing.T, ch <-chan wire.EventEnvelope, sessionID, messageID string) wire.MessageEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("hub closed")
			}
			if env.Topic != wire.EventTopicMessage {
				continue
			}
			var ev wire.MessageEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal", err)
			}
			if ev.SessionID != sessionID || ev.Op != wire.MessageChangeAppend {
				continue
			}
			if ev.Message.ID != messageID {
				continue
			}
			return ev
		case <-deadline:
			t.Fatalf("timeout waiting for append SSE of %s", messageID)
			return wire.MessageEvent{}
		}
	}
}
