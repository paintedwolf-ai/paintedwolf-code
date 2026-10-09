package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hitl"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointCreationReadParity(t *testing.T) {
	for _, kind := range []api.CheckpointKind{api.CheckpointKindToolApproval, api.CheckpointKindContentApply} {
		t.Run(string(kind), func(t *testing.T) {
			database, mgr, sid := newTestManager(t)
			insertSession(t, database, sid)
			req := hitl.CheckpointRequest{SessionID: sid, ProjectID: testdbseed.DefaultProjectID, Kind: kind}
			var response *hitl.CheckpointResponse
			var err error
			if kind == api.CheckpointKindToolApproval {
				req.ProposedAction = &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "printf fixture"}, SessionID: sid, ProjectID: req.ProjectID}
				response, err = requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), mgr, req)
				testutil.FailErr(t, "request tool approval", err)
			} else {
				req.ContentApply = &hitl.ContentApplyPayload{Tool: "write", Path: "fixture.txt", After: "fixture"}
				response, err = mgr.RequestCheckpoint(testdbseed.OwnerCaller(t, t.Context(), database), req)
				testutil.FailErr(t, "request content approval", err)
			}
			for _, restart := range []bool{false, true} {
				if restart {
					mgr = hitl.NewCheckpoints(hitl.NewSQLStore(database), nil, authzcontext.SQLRecorder(database))
					testutil.FailErr(t, "restore pending approvals", mgr.RestorePending(testdbseed.OwnerCaller(t, t.Context(), database)))
				}
				direct, err := mgr.ListPending(testdbseed.OwnerCaller(t, t.Context(), database), sid, nil)
				testutil.FailErr(t, "list direct approvals", err)
				combined, err := mgr.ListPendingForParent(testdbseed.OwnerCaller(t, t.Context(), database), sid, nil)
				testutil.FailErr(t, "list combined approvals", err)
				if len(direct) != 1 || len(combined) != 1 {
					t.Fatalf("restart=%v: direct=%d combined=%d; both must contain the created approval", restart, len(direct), len(combined))
				}
				if direct[0].ID != response.CheckpointID || combined[0].ID != response.CheckpointID {
					t.Fatalf("restart=%v: checkpoint identity changed", restart)
				}
			}
		})
	}
}

func TestChildCheckpointAttention(t *testing.T) {
	for _, parentStatus := range []string{"idle", "busy"} {
		t.Run(parentStatus, func(t *testing.T) {
			database, mgr, parent := newTestManager(t)
			insertSession(t, database, parent)
			testdbseed.InsertSession(t, database, "child", testdbseed.DefaultProjectID)
			_, err := database.ExecContext(testdbseed.OwnerCaller(t, t.Context(), database), "UPDATE sessions SET parent_session_id = ?, status = 'busy' WHERE id = 'child'", parent)
			testutil.FailErr(t, "bind child", err)
			_, err = database.ExecContext(testdbseed.OwnerCaller(t, t.Context(), database), "UPDATE sessions SET status = ? WHERE id = ?", parentStatus, parent)
			testutil.FailErr(t, "set parent state", err)
			testutil.FailErr(t, "insert worker job", worker.NewSQLStore(database).InsertTask(testdbseed.OwnerCaller(t, t.Context(), database), api.WorkerTask{
				ID: "job", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: parent, ChildSessionID: "child", Status: api.WorkerStatusRunning, AgentType: "implementer", Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal,
			}))
			response, err := requestExplicitApprovalCheckpoint(t, testdbseed.OwnerCaller(t, t.Context(), database), mgr, hitl.CheckpointRequest{
				SessionID: "child", Kind: api.CheckpointKindToolApproval,
				ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "printf fixture"}},
			})
			testutil.FailErr(t, "create child approval", err)
			combined, err := mgr.ListPendingForParent(testdbseed.OwnerCaller(t, t.Context(), database), parent, nil)
			testutil.FailErr(t, "verify child approval is in parent view", err)
			if len(combined) != 1 {
				t.Fatalf("combined approvals = %d, want 1", len(combined))
			}
			source := attention.Source{Sessions: sessionstore.NewSQL(database), Checkpoints: mgr}
			view, err := source.BuildView(testdbseed.OwnerCaller(t, t.Context(), database))
			testutil.FailErr(t, "build attention view", err)
			if len(view.Rows) != 1 || view.Rows[0].SessionID != parent || view.Rows[0].Class != api.AttentionClassNeedsYou {
				t.Fatalf("parent=%s: attention=%+v; want parent needs_you for visible child approval", parentStatus, view.Rows)
			}
			approveCurrentOption(t, testdbseed.OwnerCaller(t, t.Context(), database), mgr, "child", response.CheckpointID)
			view, err = source.BuildView(testdbseed.OwnerCaller(t, t.Context(), database))
			testutil.FailErr(t, "build resolved attention", err)
			if parentStatus == "idle" && len(view.Rows) != 0 {
				t.Fatalf("idle parent still needs attention: %+v", view.Rows)
			}
			if parentStatus == "busy" && (len(view.Rows) != 1 || view.Rows[0].Class != api.AttentionClassRunning) {
				t.Fatalf("busy parent did not resume running attention: %+v", view.Rows)
			}
		})
	}
}
