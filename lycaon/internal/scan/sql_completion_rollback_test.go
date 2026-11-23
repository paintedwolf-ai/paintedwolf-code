package scan

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanCompletionPublishesFindingSetAfterCommit(t *testing.T) {
	database := testdbfixture.Open(t, "completion.db")
	root := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, root)
	store := NewSQLStore(database)
	outbox := &progressOutbox{}
	store.SetEventOutbox(outbox)
	ctx := t.Context()
	run := authorityScan("completion", "assessment", root, "snapshot", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert scan", store.Runs.Insert(ctx, run, nil, ""))
	claimed, err := store.Runs.ClaimNext(ctx)
	testutil.FailErr(t, "claim scan", err)
	before := *claimed
	count := len(outbox.events)
	blocked := errors.New("outbox unavailable")
	outbox.err = blocked

	won, err := store.Runs.FinalizeCompleteJSON(ctx, claimed, []byte("{}"), nil, nil, api.ScanCoverageComplete)
	if won || !errors.Is(err, blocked) {
		t.Fatalf("completion = (%v, %v), want failed publication", won, err)
	}
	stored, err := store.Reads.Get(ctx, claimed.ID)
	testutil.FailErr(t, "reload scan", err)
	if stored.Status != api.CodeScanStatusRunning || stored.FindingSetID != before.FindingSetID ||
		claimed.FindingSetID != before.FindingSetID || claimed.CoverageStatus != before.CoverageStatus ||
		claimed.FindingsStored != before.FindingsStored || len(outbox.events) != count {
		t.Fatal("failed publication advanced completion")
	}

	outbox.err = nil
	won, err = store.Runs.FinalizeCompleteJSON(ctx, claimed, []byte("{}"), nil, nil, api.ScanCoverageComplete)
	testutil.FailErr(t, "retry completion", err)
	stored, err = store.Reads.Get(ctx, claimed.ID)
	testutil.FailErr(t, "reload completed scan", err)
	if !won || stored.Status != api.CodeScanStatusComplete || claimed.FindingSetID == "" ||
		stored.FindingSetID != claimed.FindingSetID || claimed.CoverageStatus != api.ScanCoverageComplete ||
		len(outbox.events) != count+1 {
		t.Fatal("retry did not publish the committed finding set")
	}
}
