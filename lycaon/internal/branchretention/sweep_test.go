package branchretention

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

type fixtureTree struct {
	jobID    string
	lastUsed time.Time
	bytes    int
}

func plantTrees(t *testing.T, branchRoot, project string, trees []fixtureTree) map[string]string {
	t.Helper()
	mgr := workspace.NewManager(branchRoot, t.TempDir())
	roots := []projectroot.RootRef{{ID: "primary", Path: project, IsPrimary: true}}
	paths := map[string]string{}
	for _, tree := range trees {
		testutil.FailErr(t, "seed project", os.WriteFile(filepath.Join(project, "payload.bin"), make([]byte, tree.bytes), 0o644))
		binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "primary", tree.jobID)
		testutil.FailErr(t, "create "+tree.jobID, err)
		workspace.TouchBranchUse(binding.Root)
		stamp := filepath.Join(enginepaths.MetaDirForBranchRoot(binding.Root), "LAST_USED")
		testutil.FailErr(t, "age "+tree.jobID, os.Chtimes(stamp, tree.lastUsed, tree.lastUsed))
		paths[tree.jobID] = binding.Root
	}
	return paths
}

func TestSweepReclaimsIdleThenBudgetOldestFirst(t *testing.T) {
	branchRoot := t.TempDir()
	project := t.TempDir()
	now := time.Now()
	paths := plantTrees(t, branchRoot, project, []fixtureTree{
		{jobID: "idle-sealed", lastUsed: now.Add(-10 * 24 * time.Hour), bytes: 64 << 10},
		{jobID: "idle-unsealed", lastUsed: now.Add(-10 * 24 * time.Hour), bytes: 64 << 10},
		{jobID: "fresh-old", lastUsed: now.Add(-2 * time.Hour), bytes: 256 << 10},
		{jobID: "fresh-new", lastUsed: now.Add(-time.Minute), bytes: 256 << 10},
	})
	var evicted []string
	deps := Deps{
		BranchRoot: branchRoot,
		Jobs: func(context.Context) (map[string]JobState, error) {
			return map[string]JobState{
				"idle-sealed": {Sealed: true}, "idle-unsealed": {Sealed: false},
				"fresh-old": {Sealed: true}, "fresh-new": {Sealed: true},
			}, nil
		},
		Evict: func(ctx context.Context, root string) error {
			evicted = append(evicted, filepath.Base(root))
			return workspace.EvictJobTree(ctx, root)
		},
		Now: func() time.Time { return now },
	}
	// Budget admits the two fresh trees but not the idle ones on top; the
	// idle unsealed tree is never a candidate.
	cfg := Config{IdleAge: 7 * 24 * time.Hour, BudgetBytes: 400 << 10, SweepInterval: time.Hour}
	report, err := Sweep(context.Background(), deps, cfg)
	testutil.FailErr(t, "sweep", err)
	if report.EvictedIdle != 1 || report.EvictedBudget != 1 {
		t.Fatalf("report = %+v evicted=%v", report, evicted)
	}
	if len(evicted) != 2 || evicted[0] != "idle-sealed" || evicted[1] != "fresh-old" {
		t.Fatalf("evicted = %v want idle-sealed then fresh-old", evicted)
	}
	for _, kept := range []string{"idle-unsealed", "fresh-new"} {
		if !workspace.BranchTreePresent(paths[kept]) {
			t.Fatalf("%s should have been kept", kept)
		}
	}
	for _, gone := range []string{"idle-sealed", "fresh-old"} {
		if workspace.BranchTreePresent(paths[gone]) {
			t.Fatalf("%s should have been reclaimed", gone)
		}
	}
}

func TestSweepSkipsLeasedTrees(t *testing.T) {
	branchRoot := t.TempDir()
	now := time.Now()
	paths := plantTrees(t, branchRoot, t.TempDir(), []fixtureTree{
		{jobID: "held", lastUsed: now.Add(-30 * 24 * time.Hour), bytes: 1024},
	})
	release, err := workspace.AcquireBranchUse(context.Background(), paths["held"])
	testutil.FailErr(t, "lease", err)
	defer release()
	deps := Deps{
		BranchRoot: branchRoot,
		Jobs: func(context.Context) (map[string]JobState, error) {
			return map[string]JobState{"held": {Sealed: true}}, nil
		},
		Evict: workspace.EvictJobTree,
		Now:   func() time.Time { return now },
	}
	report, err := Sweep(context.Background(), deps, Config{IdleAge: time.Hour, BudgetBytes: 1, SweepInterval: time.Hour})
	testutil.FailErr(t, "sweep", err)
	if report.Skipped != 1 || report.EvictedIdle != 0 || !workspace.BranchTreePresent(paths["held"]) {
		t.Fatalf("report = %+v", report)
	}
}

func TestDefaultConfigIsBounded(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.IdleAge != 7*24*time.Hour || cfg.BudgetBytes != 4<<30 || cfg.SweepInterval != time.Hour {
		t.Fatalf("bundled config = %+v", cfg)
	}
}
