package workerlane_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

type openConfinement struct{}

func (openConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (openConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }

// layeredManager is a Milestone 14 manager whose mounted view's unmount the
// test controls; the adapter is the fixture's, not a kernel mount.
func layeredManager(t *testing.T, unmount func(context.Context, string, string) error) *workspace.Manager {
	t.Helper()
	mounted := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
		UnmountViewFn: unmount,
	})
	selector := adapter.NewSelector("linux", mounted, nil, adapter.NewEagerCopyAdapter())
	selector.BudgetFor = adapter.ZeroBudget
	selector.RequireConfinement, selector.Confinement = true, openConfinement{}
	branches := t.TempDir()
	privateGit, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	return workspace.NewManager(branches).WithSelector(selector).WithPrivateGit(privateGit)
}

// Delivery seals the round without releasing the lane: the mounted view stays
// claimed for the worker's next round, and the round names the checkpoint
// Milestone 14 recorded for the branch.
func TestDeliveryOnALayeredLaneKeepsTheView(t *testing.T) {
	mgr := layeredManager(t, func(context.Context, string, string) error { return nil })
	f := newLandingFixture(t)
	f.lanes = workerlane.New(mgr, f.records)
	source := t.TempDir()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "primary", Path: source, IsPrimary: true}}
	lane, err := f.lanes.Admit(t.Context(), workerlane.Request{Actor: f.actor, Op: workerrecords.Op{ID: "layered-1"}, WorkerID: f.workerID, Facts: workerlane.Facts{Write: true}, Roots: roots, ActiveRootID: "primary"})
	testutil.FailErr(t, "prepare layered lane", err)
	t.Cleanup(func() { _ = mgr.DestroyWorkerWorkspace(context.Background(), &workspace.Binding{Root: lane.Root}) })
	if _, live := workspace.LookupBranchClaimedView(lane.Root); !live {
		t.Fatal("prepared lane has no claimed view")
	}
	f.openRound(t, "job-2")
	settled, err := f.lanes.Settle(t.Context(), workerlane.ManagerRuntime{State: workerlane.NewM14State(jobLookup{})}, workerlane.SettleRequest{
		Actor: f.actor, Op: workerrecords.Op{ID: "settle-layered"}, WorkerID: f.workerID, JobID: "job-2",
		Publish: func(context.Context, string) error { return nil },
	})
	testutil.FailErr(t, "settle", err)
	if _, live := workspace.LookupBranchClaimedView(lane.Root); !live {
		t.Fatal("delivery released the lane's view")
	}
	_, recorded, _ := workspace.BranchCheckpoint(lane.Root)
	if settled.CheckpointID != recorded {
		t.Fatalf("round names %q, branch recorded %q", settled.CheckpointID, recorded)
	}
	kept, err := f.lanes.Admit(t.Context(), workerlane.Request{Actor: f.actor, Op: workerrecords.Op{ID: "layered-2"}, WorkerID: f.workerID, Facts: workerlane.Facts{Write: true, HasLane: true}, Roots: roots, ActiveRootID: "primary"})
	testutil.FailErr(t, "next round", err)
	if kept.Root != lane.Root {
		t.Fatalf("next round moved the lane: %s -> %s", lane.Root, kept.Root)
	}
}
