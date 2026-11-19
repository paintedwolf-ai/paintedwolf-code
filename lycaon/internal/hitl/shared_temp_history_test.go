package hitl_test

import (
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"testing"
)

func TestSharedTempHistoryIncludesWorkerReportsAndHumanDenials(t *testing.T) {
	db, manager, chat := newTestManager(t)
	insertSession(t, db, chat)
	worker := "temp-review-worker"
	insertSession(t, db, worker)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	_, err := db.ExecContext(ctx, "UPDATE sessions SET parent_session_id=? WHERE id=?", chat, worker)
	testutil.FailErr(t, "attach worker", err)
	testdbseed.InsertSessionEntry(t, db, "temp-report", worker, "utterance", "temp-report", 1)
	_, err = db.ExecContext(ctx, `INSERT INTO messages(id,entry_id,session_id,role,content,origin,authority,trust_tier,seq,ord,ts,tool_result_json) VALUES('temp-report','temp-report',?,'tool','result','tool','none','untrusted',1,1,'2026-01-01T00:00:00Z',?)`, worker, `{"feedback":[{"code":"SANDBOX_SHARED_TEMP_TOLERATED","details":{"refused_shared_temp":["/var/tmp"]}}]}`)
	testutil.FailErr(t, "store delivered report", err)
	cp, err := requestExplicitApprovalCheckpoint(t, ctx, manager, hitl.CheckpointRequest{SessionID: worker, Kind: api.CheckpointKindToolApproval, ReviewKeys: []string{"sandbox:" + authzledger.AskFamilyWriteRoot + ":/tmp"}, ProposedAction: &hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "fixture"}}}})
	testutil.FailErr(t, "request worker review", err)
	_, err = manager.ResolveCheckpoint(ctx, worker, cp.CheckpointID, api.CheckpointKindToolApproval, &hitl.DecisionResult{}, nil)
	testutil.FailErr(t, "decline worker review", err)
	store := hitl.NewSQLStore(db)
	reports, declines, err := store.SharedTempHistory(ctx, chat, []string{"/tmp", "/var/tmp", "/other"})
	testutil.FailErr(t, "read restored history", err)
	if !slices.Equal(reports, []string{"/var/tmp"}) || !slices.Equal(declines, []string{"/tmp"}) {
		t.Fatalf("reports=%v declines=%v", reports, declines)
	}
}
