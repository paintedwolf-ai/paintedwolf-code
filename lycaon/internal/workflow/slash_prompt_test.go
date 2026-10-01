package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func userRowByID(t *testing.T, mgr *RunManager, sessionID, messageID string) api.Message {
	t.Helper()
	msgs, err := mgr.Sessions.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "Sessions.GetMessages", err)
	for _, msg := range msgs {
		if msg.ID == messageID {
			return msg
		}
	}
	t.Fatalf("no transcript row with id %q in %d messages", messageID, len(msgs))
	return api.Message{}
}

func TestSlashStartEchoesSubmissionIDOnUserRow(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	submissionID := uuid.NewString()

	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/plan", submissionID)
	testutil.FailErr(t, "mgr.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}
	active, err := mgr.GetActive(ctx, sessionID)
	testutil.FailErr(t, "mgr.GetActive", err)

	row := userRowByID(t, mgr, sessionID, submissionID)
	if row.Role != api.MessageRoleUser {
		t.Fatalf("role = %q want user", row.Role)
	}
	if row.Content != "/plan" {
		t.Fatalf("content = %q want /plan", row.Content)
	}
	if row.WorkflowRunID != active.ID {
		t.Fatalf("workflow_run_id = %q want %q", row.WorkflowRunID, active.ID)
	}
}

func TestSlashStartWithoutSubmissionIDStillMintsUserRow(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/plan", "")
	testutil.FailErr(t, "mgr.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}
	msgs, err := mgr.Sessions.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "Sessions.GetMessages", err)
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Content == "/plan" {
			if msg.ID == "" {
				t.Fatal("slash user row has empty id")
			}
			return
		}
	}
	t.Fatal("no /plan user row in transcript")
}

func TestSlashStartLeavesSessionStatusToTurns(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	woke := false
	mgr.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) {
		woke = true
	}
	before, err := mgr.Sessions.Get(ctx, sessionID)
	testutil.FailErr(t, "Sessions.Get before", err)

	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/plan test request", uuid.NewString())
	testutil.FailErr(t, "mgr.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}
	if !woke {
		t.Fatal("expected initial-phase wake for human start")
	}
	// Status is set by the turn: a busy start has no idle-setter when the
	// wake consumer is absent, blocking admission and quiescence.
	after, err := mgr.Sessions.Get(ctx, sessionID)
	testutil.FailErr(t, "Sessions.Get after", err)
	if after.Status != before.Status {
		t.Fatalf("status changed %q -> %q; start must not own status", before.Status, after.Status)
	}
}

func TestHumanStartLeasesStartingWorkflowActivity(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	hub := events.NewMemoryHub()
	mgr.Events = &events.Publisher{Hub: hub}
	stream, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	defer unsubscribe()

	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/plan", uuid.NewString())
	testutil.FailErr(t, "mgr.TrySlashPrompt", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}

	var edges []api.ActivityEvent
	deadline := time.After(2 * time.Second)
	for len(edges) < 2 {
		select {
		case envelope := <-stream:
			if envelope.Topic != api.EventTopicActivity {
				continue
			}
			var event api.ActivityEvent
			testutil.FailErr(t, "decode activity", json.Unmarshal(envelope.Data, &event))
			if event.Kind == api.ActivityKindStartingWorkflow {
				edges = append(edges, event)
			}
		case <-deadline:
			t.Fatalf("timeout: %d starting_workflow edges, want 2", len(edges))
		}
	}
	if edges[0].Status != api.ActivityStatusActive || edges[1].Status != api.ActivityStatusDone {
		t.Fatalf("statuses = %q, %q want active, done", edges[0].Status, edges[1].Status)
	}
	if edges[0].ActivityID == "" || edges[1].ActivityID != edges[0].ActivityID {
		t.Fatalf("activity ids = %q, %q want one non-empty id", edges[0].ActivityID, edges[1].ActivityID)
	}
	if edges[0].SessionID != sessionID {
		t.Fatalf("session_id = %q want %q", edges[0].SessionID, sessionID)
	}
}

func TestSlashExitEchoesSubmissionIDAndStampsRun(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	_, handled, err := mgr.TrySlashPrompt(ctx, sessionID, "/plan", uuid.NewString())
	testutil.FailErr(t, "mgr.TrySlashPrompt /plan", err)
	if !handled {
		t.Fatal("expected /plan slash handled")
	}
	active, err := mgr.GetActive(ctx, sessionID)
	testutil.FailErr(t, "mgr.GetActive", err)

	exitSubmissionID := uuid.NewString()
	_, handled, err = mgr.TrySlashPrompt(ctx, sessionID, "/exit", exitSubmissionID)
	testutil.FailErr(t, "mgr.TrySlashPrompt /exit", err)
	if !handled {
		t.Fatal("expected /exit slash handled")
	}

	row := userRowByID(t, mgr, sessionID, exitSubmissionID)
	if row.Role != api.MessageRoleUser {
		t.Fatalf("role = %q want user", row.Role)
	}
	if row.Content != "/exit" {
		t.Fatalf("content = %q want /exit", row.Content)
	}
	if row.WorkflowRunID != active.ID {
		t.Fatalf("workflow_run_id = %q want %q", row.WorkflowRunID, active.ID)
	}
}
