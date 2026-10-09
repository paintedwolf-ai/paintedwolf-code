package hitl_test

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointOwnershipRoutesCreationAndResolution(t *testing.T) {
	for _, transport := range []string{"direct", "outbox"} {
		for _, status := range []api.CheckpointStatus{api.CheckpointStatusApproved, api.CheckpointStatusRejected} {
			t.Run(transport+"/"+string(status), func(t *testing.T) {
				assertCheckpointLifecycleRouting(t, transport, status)
			})
		}
	}
}

func assertCheckpointLifecycleRouting(t *testing.T, transport string, status api.CheckpointStatus) {
	t.Helper()
	database, _, sid := newTestManager(t)
	insertSession(t, database, sid)
	hub := events.NewMemoryHub()
	stream, unsubscribe, err := hub.Subscribe(testdbseed.OwnerCaller(t, t.Context(), database), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project", err)
	defer unsubscribe()
	store := hitl.NewSQLStore(database)
	if transport == "outbox" {
		outbox := eventoutbox.New(database, hub)
		store.SetEventOutbox(outbox)
		outbox.Start(testdbseed.OwnerCaller(t, t.Context(), database))
		t.Cleanup(func() { testutil.FailErr(t, "close outbox", outbox.Close()) })
	}
	publisher := &events.Publisher{Hub: hub}
	manager := hitl.NewCheckpoints(store, publisher, authzcontext.SQLRecorder(database))
	response, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, hitl.CheckpointRequest{
		SessionID: sid, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "printf fixture"},
},
},
	})
	testutil.FailErr(t, "request checkpoint", err)
	stored, err := store.Get(testdbseed.OwnerCaller(t, t.Context(), database), response.CheckpointID)
	testutil.FailErr(t, "read checkpoint", err)
	if stored.ProjectID != testdbseed.DefaultProjectID {
		t.Fatalf("checkpoint project = %q", stored.ProjectID)
	}
	if _, exists := stored.Payload["project_id"]; exists {
		t.Fatal("checkpoint payload duplicates project ownership")
	}
	assertCheckpointEvent(t, stream, response.CheckpointID, api.CheckpointStatusPending)
	manager = hitl.NewCheckpoints(store, publisher, authzcontext.SQLRecorder(database))
	testutil.FailErr(t, "restore pending checkpoint", manager.RestorePending(testdbseed.OwnerCaller(t, t.Context(), database)))
	if status == api.CheckpointStatusApproved {
		approveCurrentOption(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, sid, response.CheckpointID)
	} else {
		_, err := manager.ResolveCheckpoint(testdbseed.OwnerCaller(t, t.Context(), database), sid, response.CheckpointID, api.CheckpointKindToolApproval,
			&hitl.DecisionResult{Approved: false}, nil)
		testutil.FailErr(t, "reject checkpoint", err)
	}
	assertCheckpointEvent(t, stream, response.CheckpointID, status)
}

func TestCheckpointRejectsContradictoryOwnership(t *testing.T) {
	for _, conflict := range []string{"request project", "action project", "action session", "missing session"} {
		t.Run(conflict, func(t *testing.T) {
			database, manager, sid := newTestManager(t)
			insertSession(t, database, sid)
			req := hitl.CheckpointRequest{
				SessionID: sid, Kind: api.CheckpointKindToolApproval,
				ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
},
			}
			switch conflict {
			case "request project":
				req.ProjectID = "another-project"
			case "action project":
				req.ProposedAction.Scope.ProjectID = "another-project"
			case "action session":
				req.ProposedAction.Scope.SessionID = "another-session"
			case "missing session":
				req.SessionID = "missing-session"
			}
			if _, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, req); err == nil {
				t.Fatal("checkpoint accepted contradictory ownership")
			}
			pending, err := manager.Store.ListPending(testdbseed.OwnerCaller(t, t.Context(), database))
			testutil.FailErr(t, "read pending", err)
			if len(pending) != 0 {
				t.Fatalf("rejected request persisted checkpoints: %+v", pending)
			}
		})
	}
}

func TestCheckpointDatabaseEnforcesSessionProject(t *testing.T) {
	database, manager, sid := newTestManager(t)
	insertSession(t, database, sid)
	testdbseed.InsertSession(t, database, "other-session", "other-project")
	for _, projectID := range []string{"", "other-project", "missing-project"} {
		t.Run("insert-"+projectID, func(t *testing.T) {
			err := manager.Store.Insert(testdbseed.OwnerCaller(t, t.Context(), database), hitl.StoredCheckpoint{
				ID: "invalid-" + projectID, SessionID: sid, ProjectID: projectID,
				Kind: api.CheckpointKindToolApproval, Status: hitl.DecisionStatusPending, CreatedAt: time.Now().UTC(),
			})
			if err == nil {
				t.Fatalf("database accepted project %q for session %q", projectID, sid)
			}
		})
	}
	response, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, hitl.CheckpointRequest{
		SessionID: sid, Kind: api.CheckpointKindToolApproval, ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
},
	})
	testutil.FailErr(t, "create owned checkpoint", err)
	for _, query := range []string{
		`UPDATE checkpoints SET project_id = 'other-project' WHERE id = ?`,
		`UPDATE checkpoints SET session_id = 'other-session' WHERE id = ?`,
	} {
		if _, err := database.ExecContext(testdbseed.OwnerCaller(t, t.Context(), database), query, response.CheckpointID); err == nil {
			t.Fatalf("database accepted mismatched ownership: %s", query)
		}
	}
}
