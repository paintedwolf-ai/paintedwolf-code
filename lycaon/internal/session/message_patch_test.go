package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type failSecondMessageReadStore struct {
	Store
	reads int
}

func (s *failSecondMessageReadStore) GetMessage(ctx context.Context, sessionID, messageID string) (wire.Message, error) {
	s.reads++
	if s.reads > 1 {
		return wire.Message{}, errors.New("read projection unavailable")
	}
	return s.Store.GetMessage(ctx, sessionID, messageID)
}

func TestUpdateMessageDoesNotConfirmCommittedCommandThroughReadProjection(t *testing.T) {
	ctx := t.Context()
	base := store.NewMemory()
	sess, err := base.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append message", base.AppendMessages(ctx, sess.ID, wire.Message{
		ID: "assistant-1", Role: wire.MessageRoleAssistant, Content: "draft", CreatedAt: time.Now().UTC(),
	}))
	wrapped := &failSecondMessageReadStore{Store: base}
	mgr := NewHost(wrapped, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	err = mgr.Runner.Transcript.Update(ctx, sess.ID, "assistant-1", wire.Message{
		ID: "assistant-1", Role: wire.MessageRoleAssistant, Content: "settled",
	})
	testutil.FailErr(t, "update committed message", err)
	if wrapped.reads != 1 {
		t.Fatalf("message reads = %d want one precondition read", wrapped.reads)
	}
	messages, err := base.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read committed message", err)
	if got := messages[0].Content; got != "settled" {
		t.Fatalf("content = %q want settled", got)
	}
}

func TestUpdateMessagePatchPublishesStoredRow(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "message-patch.db")

	store := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session failed", err)

	runID := "0ea26756-a763-4c2a-b933-fa6b64064a20"
	msgID := "940fad1a-398e-4fdd-8942-25f824bf4ff0"
	now := time.Now().UTC()
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at
		) VALUES (?, ?, ?, 'plan', '1.0.0', 'running', 'stub', ?, ?)
	`, runID, sess.ID, sess.ProjectID, db.FormatTime(now), db.FormatTime(now)); err != nil {
		testutil.FailErr(t, "insert workflow run failed", err)
	}
	if err := store.AppendMessages(ctx, sess.ID, wire.Message{
		ID:            msgID,
		Role:          wire.MessageRoleAssistant,
		WorkflowRunID: runID,
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		testutil.FailErr(t, "append message failed", err)
	}
	before, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "load pre-patch message", err)
	if len(before) != 1 {
		t.Fatalf("pre-patch message count = %d want 1", len(before))
	}
	beforeSeq := before[0].Seq

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe failed", err)
	defer unsub()

	if err := mgr.Runner.Transcript.Update(ctx, sess.ID, msgID, wire.Message{
		ID:      msgID,
		Role:    wire.MessageRoleAssistant,
		Content: "",
		ToolCalls: []wire.ToolCall{
			{ID: "functions.find:0", Name: "find"},
		},
	}); err != nil {
		testutil.FailErr(t, "update message failed", err)
	}

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
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			if ev.Op != wire.MessageChangePatch || ev.Message.ID != msgID {
				continue
			}
			if ev.Message.WorkflowRunID != runID {
				t.Fatalf("patch workflow_run_id = %q want %q", ev.Message.WorkflowRunID, runID)
			}
			if len(ev.Message.ToolCalls) != 1 {
				t.Fatalf("tool_calls = %d want 1", len(ev.Message.ToolCalls))
			}
			if ev.Message.Seq <= beforeSeq {
				t.Fatalf("patch event seq = %d want > %d", ev.Message.Seq, beforeSeq)
			}
			stored, getErr := store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "load patched message", getErr)
			if len(stored) != 1 || ev.Message.Seq != stored[0].Seq {
				t.Fatalf("patch event seq = %d, stored seq = %d", ev.Message.Seq, stored[0].Seq)
			}
			return
		case <-deadline:
			t.Fatal("timeout waiting for message patch event")
		}
	}
}

func TestUpdateMessagePatchPersistsCoordinatorGrounding(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "message-grounding.db")

	store := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session failed", err)

	msgID := "a-grounding-1"
	grounding := &wire.CitationGrounding{
		Traced: true,
		Checks: []wire.CitationGroundingCheck{
			{ID: "path_citations", Label: "Path citations", Status: wire.CitationGroundingCheckStatusPassed, Summary: "1 citation(s) matched leg evidence"},
		},
	}
	if err := store.AppendMessages(ctx, sess.ID, wire.Message{
		ID:         msgID,
		Role:       wire.MessageRoleAssistant,
		Content:    "The fix landed in engine.py.",
		Visibility: wire.MessageVisibilityInternal,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		testutil.FailErr(t, "append message failed", err)
	}

	if err := mgr.Runner.Transcript.Update(ctx, sess.ID, msgID, wire.Message{
		ID:         msgID,
		Role:       wire.MessageRoleAssistant,
		Content:    "The fix landed in engine.py.",
		Visibility: wire.MessageVisibilityTranscript,
		Grounding:  grounding,
	}); err != nil {
		testutil.FailErr(t, "update message failed", err)
	}

	stored, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages failed", err)
	if len(stored) != 1 {
		t.Fatalf("messages = %d want 1", len(stored))
	}
	if stored[0].Grounding == nil || !stored[0].Grounding.Traced {
		t.Fatalf("stored grounding = %+v want traced audit", stored[0].Grounding)
	}
}

func TestUpdateMessagePatchPersistsRunCompletionReportScope(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "message-report.db")

	messageStore := store.NewSQL(sqlDB)
	mgr := NewHost(messageStore, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	ctx := t.Context()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := messageStore.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	const (
		runID = "0ea26756-a763-4c2a-b933-fa6b64064a20"
		msgID = "report-draft-1"
	)
	now := time.Now().UTC()
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at
		) VALUES (?, ?, ?, 'security-survey', '1.0.0', 'running', 'report', ?, ?)
	`, runID, sess.ID, sess.ProjectID, db.FormatTime(now), db.FormatTime(now)); err != nil {
		testutil.FailErr(t, "insert workflow run", err)
	}
	testutil.FailErr(t, "append report draft", messageStore.AppendMessages(ctx, sess.ID, wire.Message{
		ID: msgID, Role: wire.MessageRoleAssistant, Visibility: wire.MessageVisibilityInternal,
		WorkflowRunID: runID, CreatedAt: now,
	}))

	meta := &wire.CompletionReportMeta{
		Scope: wire.CompletionReportScopeRun, SurfaceID: "implement_synthesis", Phase: "report",
	}
	grounding := &wire.CitationGrounding{Traced: true}
	testutil.FailErr(t, "commit report patch", mgr.Runner.Transcript.Update(ctx, sess.ID, msgID, wire.Message{
		ID: msgID, Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport,
		Content: "Grounded security report.", Visibility: wire.MessageVisibilityTranscript,
		WorkflowRunID: runID, CompletionReport: meta, Grounding: grounding,
	}))

	stored, err := messageStore.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read stored report", err)
	if len(stored) != 1 {
		t.Fatalf("messages = %d want 1", len(stored))
	}
	got := stored[0]
	if got.Kind != wire.MessageKindCompletionReport || got.CompletionReport == nil {
		t.Fatalf("stored report = %+v want completion-report metadata", got)
	}
	if got.CompletionReport.Scope != wire.CompletionReportScopeRun || got.CompletionReport.Phase != "report" {
		t.Fatalf("completion_report = %+v want run/report", got.CompletionReport)
	}
	if got.WorkflowRunID != runID || got.Grounding == nil {
		t.Fatalf("stored run/grounding = %q/%+v", got.WorkflowRunID, got.Grounding)
	}
}

func TestUpdateMessagePatchPersistsMessageKind(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "message-kind.db")

	store := store.NewSQL(sqlDB)
	ctx := context.Background()
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session failed", err)

	msgID := "a-draft-1"
	if err := store.AppendMessages(ctx, sess.ID, wire.Message{
		ID:         msgID,
		Role:       wire.MessageRoleAssistant,
		Content:    "The scan completed and the build baseline was collected.",
		Visibility: wire.MessageVisibilityInternal,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		testutil.FailErr(t, "append message failed", err)
	}

	if _, err := store.UpdateMessage(ctx, sess.ID, msgID, wire.Message{
		ID:         msgID,
		Role:       wire.MessageRoleAssistant,
		Content:    "The scan completed and the build baseline was collected.",
		Kind:       wire.MessageKindDraft,
		Visibility: wire.MessageVisibilityTranscript,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		testutil.FailErr(t, "update message failed", err)
	}

	stored, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages failed", err)
	if len(stored) != 1 {
		t.Fatalf("messages = %d want 1", len(stored))
	}
	if stored[0].Kind != wire.MessageKindDraft {
		t.Fatalf("stored kind = %q want draft", stored[0].Kind)
	}
}
