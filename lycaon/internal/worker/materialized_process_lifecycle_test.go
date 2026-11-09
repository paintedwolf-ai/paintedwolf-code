package worker

import (
	"errors"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSmallMaterializedLifecycleAcrossProcessRestart(t *testing.T) {
	runWorkerLifecycleAcrossProcessRestart(t, false)
}

func TestSmallMountedLifecycleAcrossProcessRestart(t *testing.T) {
	runWorkerLifecycleAcrossProcessRestart(t, true)
}

// Separate processes prevent registries from substituting for durable recovery.
// Process restart does not establish power-loss durability.
func runWorkerLifecycleAcrossProcessRestart(t *testing.T, mounted bool) {
	t.Helper()
	if phase := os.Getenv("PW_M14_LIFECYCLE_PHASE"); phase != "" {
		runWorkerLifecyclePhase(t, os.Getenv("PW_M14_LIFECYCLE_ROOT"), phase, mounted)
		if phase == "prepare" {
			// End the process with its published view still live; recovery must repair it.
			os.Exit(0)
		}
		return
	}
	root := t.TempDir()
	executable, err := os.Executable()
	testutil.FailErr(t, "test executable", err)
	for _, phase := range []string{"prepare", "recover-promote"} {
		cmd := osexec.CommandContext(t.Context(), executable, "-test.run=^"+t.Name()+"$", "-test.timeout=2m")
		cmd.Env = append(os.Environ(), "PW_M14_LIFECYCLE_PHASE="+phase, "PW_M14_LIFECYCLE_ROOT="+root)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s process failed: %v\n%s", phase, err, output)
		}
	}
}

func runWorkerLifecyclePhase(t *testing.T, root, phase string, mounted bool) {
	t.Helper()
	if root == "" {
		t.Fatal("missing isolated lifecycle root")
	}
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	source := filepath.Join(root, "source")
	branches := filepath.Join(testbaseline.DataDir(t, database), "worker-branches")
	gitManager, err := workspace.OpenPrivateGit(branches)
	testutil.FailErr(t, "open private Git", err)
	t.Cleanup(func() { _ = gitManager.Store().Close() })
	selector := workspace.DefaultWorkspaceSelector()
	selector.BudgetFor = func(class adapter.BudgetClass) adapter.BudgetFact {
		return adapter.BudgetFact{Class: class, Limit: 1 << 20, EntryCost: 1}
	}
	if mounted {
		confine.TestingSetAutoConfine(t)
		t.Setenv("LYCAON_CONFIG_DIR", filepath.Dir(branches))
		selector = workspace.EnforcedWorkspaceSelector()
		selector.BudgetFor = adapter.ZeroBudget
	}
	manager := workspace.NewManager(branches).WithPrivateGit(gitManager).WithSelector(selector)
	queue := NewSQLQueue(database, 4)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(root, "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(manager)
	fixture := &layeredWorkerFixture{db: database, queue: queue, manager: manager, source: source, branches: branches}
	if phase == "prepare" {
		testutil.FailErr(t, "source directory", os.MkdirAll(source, 0o755))
		testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
		gittest.InitCommit(t, source, "initial")
		testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", source)
		task := fixture.claim(t)
		snap, _, ok := WorkerClaimWorkspaceState(task.ID)
		expected := adapter.PreparationMaterialized
		if mounted {
			expected = adapter.PreparationMounted
		}
		if !ok || snap.Adapter.PreparationKind != expected {
			t.Fatalf("worker preparation = %s, want %s", snap.Adapter.PreparationKind, expected)
		}
		testutil.FailErr(t, "edit worker", os.WriteFile(filepath.Join(task.WorkspaceRoot, "main.go"), []byte("package worker\n"), 0o644))
		gittest.Run(t, task.WorkspaceRoot, "add", "main.go")
		gittest.Run(t, task.WorkspaceRoot, "commit", "-m", "worker commit")
		commit := strings.TrimSpace(gittest.Run(t, task.WorkspaceRoot, "rev-parse", "HEAD"))
		completed := fixture.completeWithChange(t, task, func() {})
		_, manifest, published, err := session.OpenTaskCheckpointStore(completed)
		testutil.FailErr(t, "checkpoint", err)
		if !published || manifest.WorkerID != task.ID {
			t.Fatal("completion published no checkpoint")
		}
		assertFileHolds(t, filepath.Join(source, "main.go"), "package main\n")
		testutil.FailErr(t, "persist phase receipt", os.WriteFile(filepath.Join(root, "phase.txt"), []byte(task.ID+"\n"+commit+"\n"), 0o600))
		return
	}
	if phase != "recover-promote" {
		t.Fatalf("unknown phase %q", phase)
	}
	raw, err := os.ReadFile(filepath.Join(root, "phase.txt"))
	testutil.FailErr(t, "read phase receipt", err)
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatalf("invalid phase receipt: %q", raw)
	}
	manager.WithRecovery(openFixtureRecovery(t, branches))
	reports, err := manager.RecoverWorkerViews(t.Context())
	testutil.FailErr(t, "recover worker views in fresh process", err)
	recoveredCheckpoint := ""
	for _, report := range reports {
		if report.WorkerID != fields[0] {
			continue
		}
		if report.Err != nil || report.Disposition != recovery.RuntimeSettled {
			t.Fatalf("worker recovery did not settle: %+v", report)
		}
		recoveredCheckpoint = report.CheckpointID
	}
	if recoveredCheckpoint == "" {
		t.Fatalf("recovery did not report the recorded worker checkpoint: %+v", reports)
	}
	task, ok := queue.Get(fields[0])
	if !ok {
		t.Fatal("restart lost worker")
	}
	assertFileHolds(t, filepath.Join(task.WorkspaceRoot, "main.go"), "package worker\n")
	if got := strings.TrimSpace(gittest.Run(t, task.WorkspaceRoot, "rev-parse", "HEAD")); got != fields[1] {
		t.Fatalf("restart changed worker Git history: got %s want %s", got, fields[1])
	}
	store, manifest, published, err := session.OpenTaskCheckpointStore(task)
	testutil.FailErr(t, "reopen checkpoint", err)
	if !published || manifest.CheckpointID != recoveredCheckpoint || len(manifest.Entries) != 1 {
		t.Fatalf("restart lost checkpoint: %+v", manifest)
	}
	base, err := store.ReadBlob(manifest.Entries[0].Base.SHA256)
	testutil.FailErr(t, "read retained merge base", err)
	if string(base) != "package main\n" {
		t.Fatalf("retained base = %q", base)
	}
	service := &MergeService{Queue: queue, Store: queue, DataDir: testbaseline.DataDir(t, database), Workspace: manager}
	testutil.FailErr(t, "recover merge transactions", service.RecoverOrphanedMergeApplies(t.Context()))
	result, err := service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote recovered checkpoint", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promotion = %+v", result)
	}
	assertFileHolds(t, filepath.Join(source, "main.go"), "package worker\n")
	testutil.FailErr(t, "cleanup worker", manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
	if _, err := os.Stat(task.WorkspaceRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cleanup retained worker tree: %v", err)
	}
	assertFileHolds(t, filepath.Join(source, "main.go"), "package worker\n")
}
