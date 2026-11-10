package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/osprocess"
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
	runWorkerLifecycleAcrossProcessRestart(t, false, false)
}

func TestSmallMountedLifecycleAcrossProcessRestart(t *testing.T) {
	runWorkerLifecycleAcrossProcessRestart(t, true, false)
}

func TestSmallMaterializedLifecyclePreservesSourceConflict(t *testing.T) {
	runWorkerLifecycleAcrossProcessRestart(t, false, true)
}

func TestSmallMountedLifecyclePreservesSourceConflict(t *testing.T) {
	runWorkerLifecycleAcrossProcessRestart(t, true, true)
}

// TestDiagnosticSmallMaterializedLifecycleCompletion10m is an opt-in diagnostic harness variant
// that retains all lifecycle assertions and production safeguards while allowing up to 10 minutes
// per child subprocess to measure full completion and phase timings. Not an acceptance gate.
func TestDiagnosticSmallMaterializedLifecycleCompletion10m(t *testing.T) {
	if os.Getenv("PW_M14_DIAGNOSTIC_COMPLETION_10M") != "1" {
		t.Skip("diagnostic 10m completion harness requires PW_M14_DIAGNOSTIC_COMPLETION_10M=1")
	}
	runWorkerLifecycleAcrossProcessRestartWithChildTimeout(t, false, false, "10m", true)
}

// Separate processes prevent registries from substituting for durable recovery.
// Process restart does not establish power-loss durability.
func runWorkerLifecycleAcrossProcessRestart(t *testing.T, mounted, sourceConflict bool) {
	t.Helper()
	runWorkerLifecycleAcrossProcessRestartWithChildTimeout(t, mounted, sourceConflict, "2m", false)
}

func runWorkerLifecycleAcrossProcessRestartWithChildTimeout(t *testing.T, mounted, sourceConflict bool, childTimeout string, diagnostic bool) {
	t.Helper()
	if phase := os.Getenv("PW_M14_LIFECYCLE_PHASE"); phase != "" {
		runWorkerLifecyclePhase(t, os.Getenv("PW_M14_LIFECYCLE_ROOT"), phase, mounted, sourceConflict)
		if phase == "prepare" && !t.Failed() {
			// End the process with its published view still live; recovery must repair it.
			os.Exit(0)
		}
		return
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if prepared := os.Getenv("PW_M14_LIFECYCLE_PREPARED_SOURCE"); prepared != "" {
		source = prepared
	} else if parent := os.Getenv("PW_M14_LIFECYCLE_SOURCE_PARENT"); parent != "" {
		var err error
		source, err = os.MkdirTemp(parent, "m14-lifecycle-source-")
		testutil.FailErr(t, "create isolated external source", err)
		t.Cleanup(func() { testutil.FailErr(t, "remove isolated external source", os.RemoveAll(source)) })
	}
	executable, err := os.Executable()
	testutil.FailErr(t, "test executable", err)
	for _, phase := range []string{"prepare", "recover-promote"} {
		if phase == "recover-promote" && sourceConflict {
			testutil.FailErr(t, "external source edit after worker exit", os.WriteFile(filepath.Join(source, "main.go"), []byte("package external\n"), 0o644))
		}
		phaseStart := time.Now()
		cmd := osexec.CommandContext(t.Context(), executable, "-test.run=^"+t.Name()+"$", "-test.timeout="+childTimeout, "-test.v=true")
		cmd.Env = append(os.Environ(), "PW_M14_LIFECYCLE_PHASE="+phase, "PW_M14_LIFECYCLE_ROOT="+root, "PW_M14_LIFECYCLE_SOURCE="+source)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s process failed (elapsed=%s): %v\n%s", phase, time.Since(phaseStart), err, output)
		}
		if diagnostic {
			t.Logf("diagnostic %s child completed in %s:\n%s", phase, time.Since(phaseStart), output)
		}
	}
}

func runWorkerLifecyclePhase(t *testing.T, root, phase string, mounted, sourceConflict bool) {
	t.Helper()
	started := time.Now()
	trace := func(step string) { t.Logf("lifecycle phase=%s step=%s elapsed=%s", phase, step, time.Since(started)) }
	trace("start")
	if root == "" {
		t.Fatal("missing isolated lifecycle root")
	}
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	source := os.Getenv("PW_M14_LIFECYCLE_SOURCE")
	if source == "" {
		t.Fatal("missing lifecycle source path")
	}
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
	recoveryRoot := filepath.Join(branches, ".adapter", "recovery")
	checkpointStore, err := checkpoint.NewStore(filepath.Join(recoveryRoot, "checkpoints"))
	testutil.FailErr(t, "open durable checkpoint store", err)
	boot, ok := osprocess.BootID()
	if !ok {
		t.Fatal("boot identity unavailable")
	}
	coordinator, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir: recoveryRoot, CheckpointStore: checkpointStore, CurrentBootID: boot,
		GitStore: gitManager.Store(), GitManager: gitManager,
		ProcessCtrl:     recovery.RecordedProcessController{},
		MountReconciler: recovery.ConnectionReconciler{Recoveries: platform.ConnectionRecoveries()},
	})
	testutil.FailErr(t, "open recovery coordinator", err)
	manager := workspace.NewManager(branches).WithPrivateGit(gitManager).WithSelector(selector).WithRecovery(coordinator)
	queue := NewSQLQueue(database, 4)
	queue.SetBaselineStore(sourceledger.New(database, filepath.Join(root, "source-content")).Baselines)
	queue.SetWorkerWorkspaceManager(manager)
	fixture := &layeredWorkerFixture{db: database, queue: queue, manager: manager, source: source, branches: branches}
	if phase == "prepare" {
		if os.Getenv("PW_M14_LIFECYCLE_PREPARED_SOURCE") == "" {
			testutil.FailErr(t, "source directory", os.MkdirAll(source, 0o755))
			testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
			gittest.InitCommit(t, source, "initial")
		} else {
			assertFileHolds(t, filepath.Join(source, "main.go"), "package main\n")
			_, err := os.Stat(filepath.Join(source, ".git"))
			testutil.FailErr(t, "prepared source Git fixture", err)
		}
		testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-id", source)
		trace("claim-start")
		task := fixture.claim(t)
		trace("claim-complete")
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
		trace("git-commit-complete")
		completed := fixture.completeWithChange(t, task, func() {})
		trace("checkpoint-complete")
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
	trace("recovery-start")
	reports, err := manager.RecoverWorkerViews(t.Context())
	trace("recovery-returned")
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
	task, lease, err := queue.EnsureWorkerBranch(t.Context(), fields[0])
	testutil.FailErr(t, "lease recovered worker branch", err)
	cleaned := false
	t.Cleanup(func() {
		lease.Release()
		if !cleaned {
			testutil.FailErr(t, "cleanup failed lifecycle", manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
		}
	})
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
	if sourceConflict {
		assertRecoveredSourceConflict(t, service, task, source, manifest.CheckpointID, fields[1])
	}
	trace("source-outage-start")
	assertWorkerSourceOutageRetry(t, service, task, source)
	trace("source-ready-promotion-start")
	stopObserver := startDiagnosticPromoteObserver(promoteTransactionDir(service.DataDir, task.ID), filepath.Join(recoveryRoot, "checkpoints", "promotions"), trace)
	result, err := service.PromoteOverlay(t.Context(), "", task.ID, api.PromoteOverlayInput{})
	stopObserver()
	trace("promotion-returned")
	testutil.FailErr(t, "promote recovered checkpoint", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promotion = %+v", result)
	}
	assertFileHolds(t, filepath.Join(source, "main.go"), "package worker\n")
	trace("post-promote-source-verified")
	lease.Release()
	trace("cleanup-destroy-start")
	testutil.FailErr(t, "cleanup worker", manager.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: task.WorkspaceRoot}))
	trace("cleanup-destroy-returned")
	cleaned = true
	if _, err := os.Stat(task.WorkspaceRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cleanup retained worker tree: %v", err)
	}
	assertFileHolds(t, filepath.Join(source, "main.go"), "package worker\n")
	trace("complete")
}

func startDiagnosticPromoteObserver(txDir, promotionsDir string, trace func(string)) func() {
	if os.Getenv("PW_M14_DIAGNOSTIC_COMPLETION_10M") != "1" {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		last := ""
		sample := func() {
			state := observeLocalPromoteState(txDir, promotionsDir)
			if state != last {
				last = state
				trace("local-journal:" + state)
			}
		}
		sample()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				sample()
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func observeLocalPromoteState(txDir, promotionsDir string) string {
	var parsed struct {
		Phase      string          `json:"phase"`
		Roots      map[string]any  `json:"roots"`
		Checkpoint json.RawMessage `json:"checkpoint"`
		Records    []string        `json:"records"`
		Targets    []any           `json:"targets"`
		Refs       []any           `json:"refs"`
	}
	txSummary := "tx=absent"
	if raw, err := os.ReadFile(filepath.Join(txDir, "transaction.json")); err == nil {
		if json.Unmarshal(raw, &parsed) == nil {
			postEntries, _ := os.ReadDir(filepath.Join(txDir, "postimages"))
			cpSet := len(parsed.Checkpoint) > 0 && string(parsed.Checkpoint) != "null"
			txSummary = fmt.Sprintf("tx=%s/roots=%d/targets=%d/refs=%d/rec=%d/cp=%t/post=%d",
				parsed.Phase, len(parsed.Roots), len(parsed.Targets), len(parsed.Refs), len(parsed.Records), cpSet, len(postEntries))
		}
	}
	promoEntries, _ := os.ReadDir(promotionsDir)
	return fmt.Sprintf("%s/promo_files=%d", txSummary, len(promoEntries))
}
