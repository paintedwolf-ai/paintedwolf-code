package api

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCachedGitHistoryRefreshesRetainedVersionMatches(t *testing.T) {
	f := newMergedHistoryFixture(t)
	before := commitsByHash(f.listing(t).Commits)[f.commitB]
	if before.MatchesVersionID != nil || before.ArrivalGitChangeID == nil {
		t.Fatalf("initial commit projection: %+v", before)
	}
	testutil.FailErr(t, "retain intermediate bytes", f.srv.Sources.SourceLedger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: f.projectID, RootID: f.rootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser, Before: []byte("landed\n"), After: []byte("intermediate\n"),
	}))
	after := commitsByHash(f.listing(t).Commits)[f.commitB]
	if after.MatchesVersionID == nil || after.ArrivalGitChangeID != nil {
		t.Fatalf("cached lineage kept stale retention projection: %+v", after)
	}
}
