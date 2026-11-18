package hitl_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A joined card stores the highest band any joiner brought; the band never
// lowers and the code follows the merge priority.
func TestSetPendingJoinedStoresTheMaxBand(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "write"},
		},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	store := hitl.NewSQLStore(sqlDB)
	stored := func() (string, string) {
		t.Helper()
		row, err := store.Get(ctx, resp.CheckpointID)
		testutil.FailErr(t, "Get", err)
		band, _ := row.Payload["consequence_band"].(string)
		code, _ := row.Payload["consequence_code"].(string)
		return band, code
	}
	join := func(band api.ConsequenceBand, code api.ConsequenceCode) {
		t.Helper()
		applied, err := store.SetPendingJoined(ctx, resp.CheckpointID, 2, []string{"call-a", "call-b"}, string(band), string(code))
		testutil.FailErr(t, "SetPendingJoined", err)
		if !applied {
			t.Fatal("SetPendingJoined did not apply to a pending card")
		}
	}

	join(api.ConsequenceBandStandard, "")
	if band, code := stored(); band != "" || code != "" {
		t.Fatalf("standard joiner stored (%q, %q), want no band", band, code)
	}
	join(api.ConsequenceBandHighRisk, api.ConsequenceCodeWriteRoot)
	if band, code := stored(); band != string(api.ConsequenceBandHighRisk) || code != string(api.ConsequenceCodeWriteRoot) {
		t.Fatalf("write-root joiner stored (%q, %q)", band, code)
	}
	join(api.ConsequenceBandHighRisk, api.ConsequenceCodeSecret)
	if band, code := stored(); band != string(api.ConsequenceBandHighRisk) || code != string(api.ConsequenceCodeSecret) {
		t.Fatalf("secret joiner stored (%q, %q)", band, code)
	}
	join(api.ConsequenceBandStandard, "")
	if band, code := stored(); band != string(api.ConsequenceBandHighRisk) || code != string(api.ConsequenceCodeSecret) {
		t.Fatalf("standard joiner lowered the stored band to (%q, %q)", band, code)
	}
}
