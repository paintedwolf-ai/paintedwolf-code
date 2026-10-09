package hitl_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointEventsFollowWorkerRegistration(t *testing.T) {
	database, _, parent := newTestManager(t)
	insertSession(t, database, parent)
	insertSession(t, database, "child")
	hub := events.NewMemoryHub()
	stream, unsubscribe, err := hub.Subscribe(testdbseed.OwnerCaller(t, t.Context(), database), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project", err)
	defer unsubscribe()
	outbox := eventoutbox.New(database, hub)
	workers := worker.NewSQLStore(database)
	workers.SetEventOutbox(outbox)
	testutil.FailErr(t, "register child worker", workers.InsertTask(testdbseed.OwnerCaller(t, t.Context(), database), api.WorkerTask{
		ID: "job", ParentSessionID: parent, ChildSessionID: "child", ProjectID: testdbseed.DefaultProjectID,
		AgentType: "implementer", Status: api.WorkerStatusRunning, Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal,
	}))
	store := hitl.NewSQLStore(database)
	store.SetEventOutbox(outbox)
	manager := hitl.NewCheckpoints(store, &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(database))
	response, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, hitl.CheckpointRequest{
		SessionID: "child", Kind: api.CheckpointKindToolApproval, ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
},
	})
	testutil.FailErr(t, "create child approval", err)
	testutil.FailErr(t, "update rationale", manager.PatchPendingToolApprovalAIRationale(testdbseed.OwnerCaller(t, t.Context(), database), response.CheckpointID, "Fixture rationale"))
	approveCurrentOption(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, "child", response.CheckpointID)
	select {
	case event := <-stream:
		t.Fatalf("event bypassed worker registration in the outbox: %+v", event)
	default:
	}
	// Restart delivery without the original wake channel.
	outbox = eventoutbox.New(database, hub)
	outbox.Start(testdbseed.OwnerCaller(t, t.Context(), database))
	t.Cleanup(func() { testutil.FailErr(t, "close outbox", outbox.Close()) })
	registration := nextCheckpointLifecycleEvent(t, stream)
	if registration.Topic != api.EventTopicWorker {
		t.Fatalf("first event = %s, want worker registration", registration.Topic)
	}
	assertCheckpointEvent(t, stream, response.CheckpointID, api.CheckpointStatusPending)
	updated := nextCheckpointLifecycleEvent(t, stream)
	var checkpoint api.CheckpointEvent
	testutil.FailErr(t, "decode updated approval", json.Unmarshal(updated.Data, &checkpoint))
	if updated.Topic != api.EventTopicCheckpoint || checkpoint.ToolApproval == nil || checkpoint.ToolApproval.AIRationale != "Fixture rationale" {
		t.Fatalf("updated checkpoint = %+v", checkpoint)
	}
	assertCheckpointEvent(t, stream, response.CheckpointID, api.CheckpointStatusApproved)
}

func nextCheckpointLifecycleEvent(t *testing.T, stream <-chan api.EventEnvelope) api.EventEnvelope {
	t.Helper()
	select {
	case event := <-stream:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint lifecycle event was not delivered")
		return api.EventEnvelope{}
	}
}

func TestCheckpointMutationRollsBackWhenEventCannotBeCommitted(t *testing.T) {
	database, _, sid := newTestManager(t)
	insertSession(t, database, sid)
	store := hitl.NewSQLStore(database)
	store.SetEventOutbox(eventoutbox.New(database, events.NewMemoryHub()))
	manager := hitl.NewCheckpoints(store, nil, authzcontext.SQLRecorder(database))
	req := hitl.CheckpointRequest{SessionID: sid, Kind: api.CheckpointKindToolApproval, ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
}}
	response, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, req)
	testutil.FailErr(t, "create initial checkpoint", err)
	_, err = database.ExecContext(testdbseed.OwnerCaller(t, t.Context(), database), `CREATE TRIGGER fail_checkpoint_event BEFORE INSERT ON event_outbox BEGIN SELECT RAISE(ABORT, 'fixture event failure'); END`)
	testutil.FailErr(t, "install event failure", err)
	if _, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), manager, req); err == nil {
		t.Fatal("checkpoint creation survived failed event commit")
	}
	if err := manager.PatchPendingToolApprovalAIRationale(testdbseed.OwnerCaller(t, t.Context(), database), response.CheckpointID, "Uncommitted rationale"); err == nil {
		t.Fatal("checkpoint update survived failed event commit")
	}
	if _, err := manager.ResolveCheckpoint(testdbseed.OwnerCaller(t, t.Context(), database), sid, response.CheckpointID, api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil); err == nil {
		t.Fatal("checkpoint resolution survived failed event commit")
	}
	if _, err := manager.Authority.ResolveApprovalOption(testdbseed.OwnerCaller(t, t.Context(), database), sid, response.CheckpointID, hitl.CurrentActionOption().ID); err == nil {
		t.Fatal("checkpoint approval survived failed event commit")
	}
	pending, err := store.ListPending(testdbseed.OwnerCaller(t, t.Context(), database))
	testutil.FailErr(t, "read pending checkpoint", err)
	if len(pending) != 1 || pending[0].ID != response.CheckpointID || pending[0].Payload["ai_rationale"] != nil {
		t.Fatalf("failed event commits changed checkpoints: %+v", pending)
	}
}
