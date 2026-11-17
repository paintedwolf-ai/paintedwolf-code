package workerlane_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

// realLane wires a lane service to a workspace manager over a one-file project.
func realLane(t *testing.T) (*roundFixture, []projectroot.RootRef) {
	t.Helper()
	f := newRoundFixture(t)
	mgr := workspace.NewManager(t.TempDir(), platform.NewSelector(platform.Config{}))
	f.lanes = workerlane.New(mgr, f.records)
	project := t.TempDir()
	testutil.FailErr(t, "seed project", os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644))
	return f, []projectroot.RootRef{{ID: "primary", Label: "primary", Path: project, IsPrimary: true}}
}

func primaryPath(t *testing.T, branchRoot string) string {
	t.Helper()
	refs, err := workspace.BranchRootRefs(branchRoot)
	testutil.FailErr(t, "branch roots", err)
	for _, r := range refs {
		if r.ID == "primary" {
			return r.Path
		}
	}
	t.Fatalf("no primary root in %s", branchRoot)
	return ""
}

// Work a round left in the lane survives a correction round; a landing gives
// the next round a fresh tree of source, and a rejection resets it.
func TestLaneKeepsWorkAcrossRoundsOnARealBranch(t *testing.T) {
	f, roots := realLane(t)
	admit := func(op string, facts workerlane.Facts) workerlane.Binding {
		t.Helper()
		b, err := f.lanes.Admit(t.Context(), workerlane.Request{Actor: f.actor, Op: workerrecords.Op{ID: op}, WorkerID: f.workerID, Facts: facts, Roots: roots, ActiveRootID: "primary"})
		testutil.FailErr(t, op, err)
		return b
	}
	// The fixture's lane came from the fake service; this lane starts real.
	first := admit("real-1", workerlane.Facts{Write: true})
	notes := filepath.Join(primaryPath(t, first.Root), "notes.txt")
	testutil.FailErr(t, "write in lane", os.WriteFile(notes, []byte("investigation\n"), 0o644))

	kept := admit("real-2", workerlane.Facts{Write: true, HasLane: true})
	if kept.Root != first.Root {
		t.Fatalf("correction moved the lane: %s -> %s", first.Root, kept.Root)
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatalf("work did not survive the correction round: %v", err)
	}

	f.round(t, "job-2", "result-2", "")
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('landed-job-1', 'job-1', '/repo', '2026-11-12T00:00:00Z')`)
	testutil.FailErr(t, "landing receipt", err)
	_, err = f.records.SetChange(t.Context(), f.actor, workerrecords.Op{ID: "land-real"}, "result-1", workerrecords.Change{State: workerrecords.ChangeLanded, LandedChangeID: "landed-job-1"})
	testutil.FailErr(t, "land", err)
	refreshed := admit("real-3", workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded, LastResultID: "result-1"})
	if refreshed.Root == first.Root || refreshed.Action != workerlane.ActionRefresh {
		t.Fatalf("landing lane = %+v", refreshed)
	}
	fresh := primaryPath(t, refreshed.Root)
	if _, err := os.Stat(filepath.Join(fresh, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("fresh lane replayed the old tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "main.go")); err != nil {
		t.Fatalf("fresh lane lacks source: %v", err)
	}
}
