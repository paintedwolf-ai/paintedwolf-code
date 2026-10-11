package historyretention

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDirectoryTombstoneDoesNotPermanentlyPinChildContent(t *testing.T) {
	s := newTestService(t)
	ledger := sourceledger.New(s.Database, t.TempDir())
	project := testdbseed.DefaultProjectID
	testdbseed.InsertProjectRootWithID(t, s.Database, project, "root", t.TempDir())
	testutil.FailErr(t, "record child", ledger.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: "root", Path: "tree/child"},
		ProjectID:      project, Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate, After: []byte("retained"),
	}))
	head, err := ledger.History.ResolveHead(t.Context(), project, sourcebranch.Trunk, "root", "tree/child")
	testutil.FailErr(t, "resolve child", err)
	eligible := func() bool {
		t.Helper()
		candidates, err := listCandidates(t.Context(), s.Database, "source_revisions", project, "2100-01-01T00:00:00Z", "", "")
		testutil.FailErr(t, "read retention candidates", err)
		for _, candidate := range candidates {
			if candidate.ID == head.VersionID {
				return true
			}
		}
		return false
	}
	if eligible() {
		t.Fatal("live child content became eligible for pruning")
	}
	testutil.FailErr(t, "remove parent", ledger.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: "root", Path: "tree", EntryKind: sourceledger.EntryKindDirectory},
		ProjectID:      project, Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpDelete,
	}))
	if !eligible() {
		t.Fatal("absent child content remains permanently pinned")
	}
}
