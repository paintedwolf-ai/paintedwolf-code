package workerlane_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// realLane wires a lane service to a Milestone 14 workspace manager over a
// one-file project.
func realLane(t *testing.T) (*landingFixture, *workspace.Manager, []projectroot.RootRef) {
	t.Helper()
	f := newLandingFixture(t)
	mgr := workspace.NewManager(t.TempDir())
	f.lanes = workerlane.New(mgr, f.records)
	project := t.TempDir()
	testutil.FailErr(t, "seed project", os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644))
	return f, mgr, []projectroot.RootRef{{ID: "primary", Path: project, IsPrimary: true}}
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
	f, mgr, roots := realLane(t)
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

	runtime := workerlane.ManagerRuntime{Manager: mgr}
	f.openRound(t, "job-2")
	settled, err := f.lanes.Settle(t.Context(), runtime, workerlane.SettleRequest{
		Actor: f.actor, Op: workerrecords.Op{ID: "settle-real"}, WorkerID: f.workerID, JobID: "job-2",
		Publish: func(context.Context, string) error { return nil },
	})
	testutil.FailErr(t, "settle on a real branch", err)
	if settled.Ordinal != 2 {
		t.Fatalf("settled = %+v", settled)
	}

	out, err := f.lanes.Land(t.Context(), f.promoter, workerlane.Change{Actor: f.actor, Op: workerrecords.Op{ID: "land-real"}, ResultID: "result-1", Roots: roots, ActiveRootID: "primary"}, api.PromoteOverlayInput{})
	testutil.FailErr(t, "land", err)
	if out.LaneErr != nil || out.Lane.Root == first.Root {
		t.Fatalf("landing lane = %+v", out)
	}
	fresh := primaryPath(t, out.Lane.Root)
	if _, err := os.Stat(filepath.Join(fresh, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("fresh lane replayed the old tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "main.go")); err != nil {
		t.Fatalf("fresh lane lacks source: %v", err)
	}
}
