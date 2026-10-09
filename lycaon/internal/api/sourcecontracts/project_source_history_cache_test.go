package sourcecontracts

import (
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCachedGitHistoryRefreshesRetainedVersionMatches(t *testing.T) {
	f := contractfixture.NewMergedHistoryFixture(t)
	before := contractfixture.CommitsByHash(f.Listing(t).Commits)[f.CommitB]
	if before.MatchesVersionID != nil || before.ArrivalGitChangeID == nil {
		t.Fatalf("initial commit projection: %+v", before)
	}
	testutil.FailErr(t, "retain intermediate bytes", f.Srv.Sources.Workspace.SourceLedger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: f.ProjectID, RootID: f.RootID, Path: "src/app.ts", Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginUser, Before: []byte("landed\n"), After: []byte("intermediate\n"),
	}))
	after := contractfixture.CommitsByHash(f.Listing(t).Commits)[f.CommitB]
	if after.MatchesVersionID == nil || after.ArrivalGitChangeID != nil {
		t.Fatalf("cached lineage kept stale retention projection: %+v", after)
	}
}
