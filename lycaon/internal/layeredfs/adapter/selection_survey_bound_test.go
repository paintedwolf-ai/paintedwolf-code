package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func wideDirectory(t *testing.T, files int) string {
	t.Helper()
	root := t.TempDir()
	wide := filepath.Join(root, "wide")
	testutil.FailErr(t, "mkdir", os.Mkdir(wide, 0o755))
	for i := range files {
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(wide, fmt.Sprintf("f%05d", i)), nil, 0o644))
	}
	return root
}

// A survey that passes its limit inside one very wide directory stops within a batch of the limit,
// asserted by the entries it read.
func TestSurveyOfAWideDirectoryReadsWithinOneBatchOfItsLimit(t *testing.T) {
	t.Parallel()
	root := wideDirectory(t, 8*surveyBatch)
	fact := BudgetFact{Limit: 2 * 20, EntryCost: 1}
	roots := []rootCostFacts{{grant: RootGrant{RootID: "root", SourcePath: root}, clonable: true}}
	cost, err := countMaterializeCost(t.Context(), roots, fact, true)
	testutil.FailErr(t, "survey", err)
	if !cost.Partial {
		t.Fatalf("an over-limit survey was not partial: %+v", cost)
	}
	if limit := int64(20 + 2*surveyBatch); cost.EntriesRead > limit {
		t.Fatalf("survey read %d entries, want at most %d", cost.EntriesRead, limit)
	}
}

// The clone probe's search for a sample file reads no more than one batch of a wide directory.
func TestSampleFileSearchReadsOneBatch(t *testing.T) {
	t.Parallel()
	root := wideDirectory(t, 8*surveyBatch)
	sample, read, err := FirstRegularFile(t.Context(), filepath.Join(root, "wide"), nil)
	testutil.FailErr(t, "search", err)
	if sample == "" {
		t.Fatal("no sample found")
	}
	if read > surveyBatch {
		t.Fatalf("search read %d entries, want at most %d", read, surveyBatch)
	}
}

// A root with index facts is costed from them and its tree is not read.
func TestSurveyUsesIndexFactsWithoutReading(t *testing.T) {
	t.Parallel()
	root := wideDirectory(t, 4)
	roots := []rootCostFacts{{grant: RootGrant{RootID: "root", SourcePath: root, Estimate: &SourceIndexEstimate{EstimatedEntries: 5, EstimatedBytes: 0}}, clonable: true}}
	cost, err := countMaterializeCost(t.Context(), roots, RecordedBudget(BudgetClass{Platform: "linux", Filesystem: FilesystemClonable}), true)
	testutil.FailErr(t, "survey", err)
	if cost.Source != CostFromIndex || cost.EntriesRead != 0 || cost.Entries != 5 {
		t.Fatalf("cost = %+v, want the index facts and no reads", cost)
	}
}
