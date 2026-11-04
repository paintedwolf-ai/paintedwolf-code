package delegations

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/branchretention"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// [process-crash evidence, not physical power-loss proof] Every phase crashes after its durable
// begin; the next engine exposes only the complete checkpoint and preserves interrupted work.
func TestRestartAfterCrashInEveryPhaseExposesOnlyCompleteCheckpoints(t *testing.T) {
	for _, phase := range []recovery.TransitionPhase{
		recovery.PhasePreparation, recovery.PhaseActiveMutation, recovery.PhaseBarrierFlush, recovery.PhasePublication,
		recovery.PhaseRefImport, recovery.PhaseFilePromotion, recovery.PhaseRelease,
	} {
		t.Run(string(phase), func(t *testing.T) {
			f := newEngineFixture(t)
			task := f.claimMountedWorker()
			view := f.liveView(task.WorkspaceRoot)
			mutate(t, view, "checkpoint-A\n")
			testutil.FailErr(t, "delete in A", view.Core.Unlink(layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: "keep.txt"}, false))
			f.publish(task, view, "cp-A")

			// Later work overwrites A's bytes and recreates what A deleted, then the process dies.
			mutate(t, view, "unsealed-after-A\n")
			_, err := view.Core.WriteFile(layeredfs.QualifiedPath{RootID: fixtureRootID, Rel: "keep.txt"}, []byte("recreated\n"), 0o644)
			testutil.FailErr(t, "recreate", err)
			if phase == recovery.PhasePublication {
				_, pubErr := f.publisher(task, view).Publish(t.Context(), checkpoint.PublishRequest{
					CheckpointID: "cp-half",
					FaultInjector: func(stage checkpoint.FaultStage) error {
						if stage == checkpoint.FaultBeforeManifestSync {
							return errSimulatedCrash
						}
						return nil
					},
				})
				if !errors.Is(pubErr, errSimulatedCrash) {
					t.Fatalf("publication fault: %v", pubErr)
				}
			}
			testutil.FailErr(t, "journal begin", f.engine.Recovery.RecordTransition(task.ID, view.ViewID, phase, recovery.StateBegin, "cp-A", "op-"+string(phase),
				[]layeredfs.QualifiedPath{primaryPath}, phase == recovery.PhaseActiveMutation))

			engine := f.boot()
			testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))

			if got := f.script.calls(); len(got) != 1 || got[0] != task.ID {
				t.Fatalf("settled connections = %v", got)
			}
			if got := f.readBranch(task.WorkspaceRoot, "app.txt"); got != "checkpoint-A\n" {
				t.Fatalf("resumed branch shows %q", got)
			}
			if _, err := os.Stat(filepath.Join(task.WorkspaceRoot, "keep.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("checkpoint A's deletion was not restored: %v", err)
			}
			journal, err := engine.Recovery.LoadWorkerJournal(task.ID)
			testutil.FailErr(t, "journal", err)
			if journal.LastPublishedCheckpointID != "cp-A" || journal.ActivePhase != "" {
				t.Fatalf("journal after recovery %+v", journal)
			}
			if _, err := engine.Recovery.VerifyCheckpointReconstructible(t.Context(), "cp-half"); err == nil {
				t.Fatal("a half-written checkpoint must not read as complete")
			}
			records, err := engine.Recovery.ListInterruptedRecords(task.ID)
			testutil.FailErr(t, "interrupted records", err)
			if len(records) != 1 || records[0].InterruptedPhase != phase || !slices.Contains(records[0].AffectedPaths, primaryPath) {
				t.Fatalf("interrupted records %+v", records)
			}
			if !quarantineHolds(t, records[0].QuarantineDir, "unsealed-after-A\n") {
				t.Fatal("interrupted bytes were not preserved")
			}

			// The killed view keeps its one record. The rebuilt view was not released either, so
			// it reports unknown scope, and holds no second copy of what the checkpoint has.
			engine = f.boot()
			testutil.FailErr(t, "repeat startup recovery", engine.RecoverWorkerViews(t.Context()))
			again, err := engine.Recovery.ListInterruptedRecords(task.ID)
			testutil.FailErr(t, "interrupted records", err)
			if len(again) != 2 || again[0].ViewID == again[1].ViewID {
				t.Fatalf("retry duplicated salvage: %+v", again)
			}
			for _, record := range again {
				if record.ID == records[0].ID {
					continue
				}
				if !record.UnknownScope || quarantineHolds(t, record.QuarantineDir, "checkpoint-A\n") {
					t.Fatalf("rebuilt view record: %+v", record)
				}
			}
		})
	}
}

// [process-crash evidence, not physical power-loss proof] Promotion faults inside the real apply
// leave the checkpoint and its claim for repair, and no landing is recorded.
func TestFailedPromotionKeepsCheckpointForRepairAcrossRestart(t *testing.T) {
	for _, step := range []string{"mid_file_apply", "before_ref_import"} {
		t.Run(step, func(t *testing.T) {
			f := newEngineFixture(t)
			task := f.claimMountedWorker()
			view := f.liveView(task.WorkspaceRoot)
			mutate(t, view, "checkpoint-A\n")
			f.publish(task, view, "cp-A")

			f.faults = func(_ recovery.TransitionPhase, at string) error {
				if at == step {
					return errSimulatedCrash
				}
				return nil
			}
			engine := f.boot()
			targets := map[string]string{fixtureRootID: f.project}
			promoter, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{Store: checkpointStore(f), TargetRoots: targets})
			testutil.FailErr(t, "promoter", err)
			request := recovery.StackedPromotionRequest{ChildCheckpointID: "cp-A", TargetRoots: targets, Promoter: promoter}
			if _, err := engine.Recovery.PromoteStackedCheckpoint(t.Context(), request); !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("promotion fault: %v", err)
			}

			f.faults = nil
			engine = f.boot()
			testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))
			if _, err := engine.Recovery.VerifyCheckpointReconstructible(t.Context(), "cp-A"); err != nil {
				t.Fatalf("failed promotion lost its checkpoint: %v", err)
			}
			claims, err := engine.Recovery.ListClaims()
			testutil.FailErr(t, "claims", err)
			if !slices.ContainsFunc(claims, func(c recovery.RetentionClaim) bool { return c.Kind == recovery.ClaimPromotion }) {
				t.Fatalf("promotion claim gone after failure: %+v", claims)
			}
			if _, landed, err := engine.Recovery.LoadLandingReceipt("cp-A"); err != nil || landed {
				t.Fatalf("failed promotion recorded a landing: landed=%t err=%v", landed, err)
			}
		})
	}
}

func quarantineHolds(t *testing.T, dir, want string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found = found || string(raw) == want
		}
		return nil
	})
	testutil.FailErr(t, "walk quarantine", err)
	return found
}

func TestRestartWithoutCheckpointFailsWorkerWithTypedReasonAndKeepsWork(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "only-copy\n")
	journalFromBranch(f, task)

	engine := f.boot()
	testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))

	got, ok := engine.Queue.Get(task.ID)
	if !ok || got.Status != api.WorkerStatusFailed {
		t.Fatalf("task after recovery %+v", got)
	}
	records, err := engine.Recovery.ListInterruptedRecords(task.ID)
	testutil.FailErr(t, "interrupted", err)
	if len(records) != 1 || !quarantineHolds(t, records[0].QuarantineDir, "only-copy\n") {
		t.Fatalf("only copy not preserved: %+v", records)
	}
}

func TestRestartWithUnresolvedConnectionRetainsDataAndRetries(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "checkpoint-A\n")
	f.publish(task, view, "cp-A")
	mutate(t, view, "unsealed\n")

	f.script.set(adapter.ConnectionServerAlive)
	engine := f.boot()
	testutil.FailErr(t, "startup keeps serving past one stuck worker", engine.RecoverWorkerViews(t.Context()))
	if idents, err := engine.Recovery.ListMountIdentities(task.ID); err != nil || len(idents) != 1 {
		t.Fatalf("identity must survive for retry: %+v err=%v", idents, err)
	}
	if claims := unresolvedClaims(t, engine, task.ID); len(claims) != 1 {
		t.Fatalf("unresolved custody claims %+v", claims)
	}
	if !workspace.BranchTreePresent(task.WorkspaceRoot) {
		t.Fatal("an unresolved branch must not be marked for rebuild")
	}
	fenced, ok := engine.Queue.Get(task.ID)
	if !ok || fenced.Status != api.WorkerStatusRunning {
		t.Fatalf("task after unresolved recovery %+v", fenced)
	}
	var unresolved *recovery.UnresolvedSettlementError
	if err := engine.Queue.Cancel(t.Context(), task.ID, nil); !errors.As(err, &unresolved) || !unresolved.RetainedCustody {
		t.Fatalf("cancel during unresolved settlement: %v", err)
	}
	if got, _ := engine.Queue.Get(task.ID); got.Status.IsTerminal() {
		t.Fatalf("cancellation committed over unresolved settlement: %+v", got)
	}
	if _, err := os.Stat(task.WorkspaceRoot); err != nil {
		t.Fatalf("data deleted during unresolved settlement: %v", err)
	}
	if err := engine.Workspace.EvictWorkerBranch(t.Context(), task.WorkspaceRoot); !errors.Is(err, workspace.ErrBranchRetained) {
		t.Fatalf("eviction during unresolved settlement: %v", err)
	}

	f.script.set(adapter.ConnectionDetached)
	testutil.FailErr(t, "retry cancellation", engine.Queue.Cancel(t.Context(), task.ID, nil))
	if got, _ := engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
		t.Fatalf("retry did not finish: %+v", got)
	}
	if claims := unresolvedClaims(t, engine, task.ID); len(claims) != 0 {
		t.Fatalf("settled custody claims remain %+v", claims)
	}
	if records, err := engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 1 || !quarantineHolds(t, records[0].QuarantineDir, "unsealed\n") {
		t.Fatalf("unsealed work after cancellation: %+v err=%v", records, err)
	}
	if _, err := engine.Recovery.VerifyCheckpointReconstructible(t.Context(), "cp-A"); err != nil {
		t.Fatalf("release deleted the retained checkpoint: %v", err)
	}
}

func unresolvedClaims(t *testing.T, engine *Runtime, workerID string) []recovery.RetentionClaim {
	t.Helper()
	claims, err := engine.Recovery.ListClaims()
	testutil.FailErr(t, "list claims", err)
	var out []recovery.RetentionClaim
	for _, claim := range claims {
		if claim.Kind == recovery.ClaimUnresolvedSettlement && claim.WorkerID == workerID {
			out = append(out, claim)
		}
	}
	return out
}

func TestProjectReconcileSettlesBeforeDeletingAndLeavesLiveWorkersAlone(t *testing.T) {
	f := newEngineFixture(t)
	stale := f.claimMountedWorker()
	live := f.claimMountedWorker()
	mergeWorker(t, f, stale)
	f.boot()

	f.script.onSettle = func(rec adapter.RecordedConnection) {
		if rec.ViewID == stale.ID {
			if _, err := os.Stat(stale.WorkspaceRoot); err != nil {
				t.Errorf("sandbox deleted before its mount settled: %v", err)
			}
		}
	}
	f.script.set(adapter.ConnectionServerAlive)
	removed, err := f.engine.ReconcileWorkerSandboxes(t.Context(), []string{f.project})
	var unresolved *recovery.UnresolvedSettlementError
	if !errors.As(err, &unresolved) || removed != 0 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if _, statErr := os.Stat(stale.WorkspaceRoot); statErr != nil {
		t.Fatalf("unresolved sandbox removed: %v", statErr)
	}

	f.script.set(adapter.ConnectionDetached)
	removed, err = f.engine.ReconcileWorkerSandboxes(t.Context(), []string{f.project})
	testutil.FailErr(t, "retry reconcile", err)
	if removed == 0 {
		t.Fatal("settled sandbox was not removed")
	}
	if _, statErr := os.Stat(stale.WorkspaceRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("settled sandbox survives: %v", statErr)
	}
	if slices.Contains(f.script.calls(), live.ID) {
		t.Fatal("reconciliation of a serving engine settled a live worker")
	}
	if _, statErr := os.Stat(live.WorkspaceRoot); statErr != nil {
		t.Fatalf("live sandbox removed: %v", statErr)
	}
}

func mergeWorker(t *testing.T, f *engineFixture, task *api.WorkerTask) {
	t.Helper()
	_, err := f.database.ExecContext(t.Context(), `UPDATE worker_jobs SET merge_status = 'merged', status = 'complete' WHERE id = ?`, task.ID)
	testutil.FailErr(t, "mark merged", err)
}

func TestCancelReleasesAViewThisEngineServesThroughItsAdapter(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "unsealed\n")

	testutil.FailErr(t, "cancel", f.engine.Queue.Cancel(t.Context(), task.ID, nil))

	if got, _ := f.engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
		t.Fatalf("cancel: %+v", got)
	}
	if len(f.script.calls()) != 0 {
		t.Fatal("a view this engine serves must release through its adapter, not recovery")
	}
	if idents, err := f.engine.Recovery.ListMountIdentities(task.ID); err != nil || len(idents) != 0 {
		t.Fatalf("identity after release: %+v err=%v", idents, err)
	}
	if records, err := f.engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 1 || !quarantineHolds(t, records[0].QuarantineDir, "unsealed\n") {
		t.Fatalf("unsealed work after cancellation: %+v err=%v", records, err)
	}
}

func TestRunStopRoutesThroughSettlementBeforeCommit(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	const runID = "run-under-stop"
	_, err := f.database.ExecContext(t.Context(), `UPDATE worker_jobs SET workflow_run_id = ? WHERE id = ?`, runID, task.ID)
	testutil.FailErr(t, "bind run", err)
	f.boot()
	stop := &worker.RunStopService{Queue: f.engine.Queue}

	f.script.set(adapter.ConnectionForeign)
	var unresolved *recovery.UnresolvedSettlementError
	if err := stop.CancelWorkersByRunID(t.Context(), runID, "stop"); !errors.As(err, &unresolved) {
		t.Fatalf("run stop over unresolved mount: %v", err)
	}
	if got, _ := f.engine.Queue.Get(task.ID); got.Status.IsTerminal() {
		t.Fatalf("run stop committed over unresolved mount: %+v", got)
	}
	f.script.set(adapter.ConnectionDetached)
	testutil.FailErr(t, "retry run stop", stop.CancelWorkersByRunID(t.Context(), runID, "stop"))
	if got, _ := f.engine.Queue.Get(task.ID); got.Status != api.WorkerStatusCanceled {
		t.Fatalf("run stop after settlement: %+v", got)
	}
}

func TestRetentionEvictsOnlyWhatARetainedCheckpointCanRebuild(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "only-copy\n")
	journalFromBranch(f, task)

	engine := f.boot()
	report := sweepSealed(t, engine, task.ID)
	if report.Skipped != 1 || report.EvictedIdle+report.EvictedBudget != 0 {
		t.Fatalf("recorded view must not evict: %+v", report)
	}

	// Settled, but its working data is the only copy: still refused.
	f.script.set(adapter.ConnectionAbsent)
	reports := engine.Recovery.RecoverWorkerRuntime(t.Context(), task.ID)
	if !reports.Deletable() || reports.Reason != recovery.ReasonNoCheckpoint {
		t.Fatalf("settlement: %+v", reports)
	}
	report = sweepSealed(t, engine, task.ID)
	if report.Skipped != 1 {
		t.Fatalf("only unsealed result must not evict: %+v", report)
	}
	if _, err := os.Stat(task.WorkspaceRoot); err != nil {
		t.Fatalf("tree gone: %v", err)
	}
}

func TestRetentionEvictsVerifiedCheckpointAndLeaseRebuildsIt(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "checkpoint-A\n")
	f.publish(task, view, "cp-A")

	// A settled worker that has not resumed: an active one holds its rebuilt view and is retained.
	engine := f.boot()
	settled := engine.Recovery.RecoverWorkerRuntime(t.Context(), task.ID)
	if !settled.Deletable() || settled.Interrupted == nil || !settled.Interrupted.UnknownScope {
		t.Fatalf("settlement: %+v", settled)
	}
	report := sweepSealed(t, engine, task.ID)
	if report.EvictedIdle+report.EvictedBudget != 1 {
		t.Fatalf("sweep %+v", report)
	}
	if _, err := os.Stat(task.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree survives eviction: %v", err)
	}
	_, lease, err := engine.Queue.EnsureWorkerBranch(t.Context(), task.ID)
	testutil.FailErr(t, "rebuild from checkpoint", err)
	defer lease.Release()
	if got := f.readBranch(task.WorkspaceRoot, "app.txt"); got != "checkpoint-A\n" {
		t.Fatalf("rebuilt branch %q", got)
	}
}

func sweepSealed(t *testing.T, engine *Runtime, jobID string) branchretention.Report {
	t.Helper()
	deps := engine.BranchRetentionDeps()
	deps.Jobs = func(context.Context) (map[string]branchretention.JobState, error) {
		return map[string]branchretention.JobState{jobID: {Sealed: true}}, nil
	}
	report, err := branchretention.ReclaimSealed(t.Context(), deps)
	testutil.FailErr(t, "sweep", err)
	return report
}

func TestStartupCollectsOnlyUnreferencedRetainedData(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "checkpoint-A\n")
	f.publish(task, view, "cp-A")
	store := checkpointStore(f)
	orphanBlob := filepath.Join(store.RootDir(), "blobs", "0000000000000000000000000000000000000000000000000000000000000000")
	testutil.FailErr(t, "seed orphan blob", os.WriteFile(orphanBlob, []byte("orphan"), 0o600))
	staged := filepath.Join(store.RootDir(), "checkpoints", "cp-staged")
	testutil.FailErr(t, "seed staged checkpoint", os.MkdirAll(staged, 0o700))
	testutil.FailErr(t, "seed staged manifest", os.WriteFile(filepath.Join(staged, "manifest.json.tmp"), []byte("{}"), 0o600))

	engine := f.boot()
	testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))

	if _, err := os.Stat(orphanBlob); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced blob retained: %v", err)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged checkpoint retained: %v", err)
	}
	if _, err := engine.Recovery.VerifyCheckpointReconstructible(t.Context(), "cp-A"); err != nil {
		t.Fatalf("collection broke a retained checkpoint: %v", err)
	}
}

func TestInvalidMetadataIsRefusedAndRetainedThroughEveryDeletionPath(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	journalFromBranch(f, task)
	path := filepath.Join(f.engine.Recovery.RootDir(), "journals", task.ID+".json")
	future := []byte(`{"format_version":99,"worker_id":"` + task.ID + `","project_id":"p"}`)
	testutil.FailErr(t, "seed future journal", os.WriteFile(path, future, 0o600))

	engine := f.boot()
	testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))
	mergeWorker(t, f, task)
	if _, err := engine.ReconcileWorkerSandboxes(t.Context(), []string{f.project}); !errors.Is(err, recovery.ErrUnsupportedFormatVersion) {
		t.Fatalf("reconcile with unknown journal: %v", err)
	}
	if _, err := os.Stat(task.WorkspaceRoot); err != nil {
		t.Fatalf("refused worker's sandbox removed: %v", err)
	}
	if err := engine.Workspace.EvictWorkerBranch(t.Context(), task.WorkspaceRoot); !errors.Is(err, workspace.ErrBranchRetained) {
		t.Fatalf("eviction with unknown journal: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != string(future) {
		t.Fatalf("unknown journal changed: %q err=%v", raw, err)
	}
}

func TestDiskFullDuringRecoveryKeepsLastResultAndRetries(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "checkpoint-A\n")
	f.publish(task, view, "cp-A")
	mutate(t, view, "unsealed\n")

	full := true
	f.writeHook = func(_, name string, _ []byte) error {
		if full {
			return syscall.ENOSPC
		}
		return nil
	}
	engine := f.boot()
	err := engine.RecoverWorkerViews(t.Context())
	if err != nil && !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("recovery on a full disk: %v", err)
	}
	full = false
	if _, statErr := os.Stat(task.WorkspaceRoot); statErr != nil {
		t.Fatalf("disk-full recovery removed data: %v", statErr)
	}
	engine = f.boot()
	testutil.FailErr(t, "retry startup recovery", engine.RecoverWorkerViews(t.Context()))
	if got := f.readBranch(task.WorkspaceRoot, "app.txt"); got != "checkpoint-A\n" {
		t.Fatalf("branch after retry %q", got)
	}
	if records, err := engine.Recovery.ListInterruptedRecords(task.ID); err != nil || len(records) != 1 || !quarantineHolds(t, records[0].QuarantineDir, "unsealed\n") {
		t.Fatalf("unsealed work after retry: %+v err=%v", records, err)
	}
}

// [process-crash evidence, not physical power-loss proof] Same-boot writers settle before
// recovery proceeds, and a process that only shares a PID value is never signaled.
func TestRestartSettlesSurvivingRecordedWriterAndNothingElse(t *testing.T) {
	writer := startSleeper(t)
	bystander := startSleeper(t)
	writerIdentity, err := adapter.CurrentProcessIdentity(writer.Process.Pid)
	if err != nil {
		t.Skipf("no process identity on this platform: %v", err)
	}
	bystanderIdentity, err := adapter.CurrentProcessIdentity(bystander.Process.Pid)
	testutil.FailErr(t, "bystander identity", err)

	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "checkpoint-A\n")
	f.publish(task, view, "cp-A")
	testutil.FailErr(t, "record writer", f.engine.Recovery.RecordWorkerProcess(task.ID, task.ID, writerIdentity))
	reused := adapter.ProcessIdentity{PID: bystanderIdentity.PID, Started: bystanderIdentity.Started + 1, BootID: bystanderIdentity.BootID}
	testutil.FailErr(t, "record reused pid", f.engine.Recovery.RecordWorkerProcess(task.ID, task.ID, reused))

	engine := f.boot()
	testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))
	if !writerIdentity.Ended() {
		t.Fatal("recorded writer survived settlement")
	}
	if bystanderIdentity.Ended() {
		t.Fatal("a process that only shares a recorded PID was stopped")
	}
}

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "read _")
	stdin, err := cmd.StdinPipe()
	testutil.FailErr(t, "sleeper stdin", err)
	testutil.FailErr(t, "start sleeper", cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); <-done })
	return cmd
}

// Fixture evidence: the rebuild runs on the fixture mounted adapter, with no kernel mount.
func TestRebuiltMountedViewPublishesAndSettlesLikeAClaimedOne(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	view := f.liveView(task.WorkspaceRoot)
	mutate(t, view, "checkpoint-A\n")
	f.publish(task, view, "cp-A")

	engine := f.boot()
	engine.Workspace.WithSelector(mountedSelector())
	testutil.FailErr(t, "startup recovery", engine.RecoverWorkerViews(t.Context()))

	root := task.WorkspaceRoot
	if _, ok := consumer.LookupBranchEngine(root); !ok {
		t.Fatal("rebuilt view has no consumer engine")
	}
	rebuilt := f.liveView(root)
	if rebuilt.ViewID == view.ViewID {
		t.Fatalf("view %s was not rebuilt", rebuilt.ViewID)
	}
	mutate(t, rebuilt, "after-rebuild\n")
	handled, err := workspace.PublishBranchCheckpoint(t.Context(), root)
	testutil.FailErr(t, "publish rebuilt view", err)
	journal, err := engine.Recovery.LoadWorkerJournal(task.ID)
	testutil.FailErr(t, "journal", err)
	_, recorded, ok := workspace.BranchCheckpoint(root)
	if !handled || !ok || journal.LastPublishedCheckpointID != recorded || recorded == "cp-A" {
		t.Fatalf("handled=%v recorded=%q journal=%q", handled, recorded, journal.LastPublishedCheckpointID)
	}

	testutil.FailErr(t, "destroy", engine.Workspace.DestroyWorkerWorkspace(&workspace.Binding{ID: task.ID, Root: root}))
	if _, ok := consumer.LookupBranchEngine(root); ok {
		t.Fatal("settlement left the rebuilt view's engine registered")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("branch remains: %v", err)
	}
}

func TestSettlingALiveViewWithdrawsItsEngine(t *testing.T) {
	f := newEngineFixture(t)
	task := f.claimMountedWorker()
	if _, ok := consumer.LookupBranchEngine(task.WorkspaceRoot); !ok {
		t.Fatal("claim registered no engine")
	}
	testutil.FailErr(t, "settle", f.engine.Workspace.SettleBranchRuntime(t.Context(), task.ID, task.WorkspaceRoot))
	if _, ok := consumer.LookupBranchEngine(task.WorkspaceRoot); ok {
		t.Fatal("settlement left the engine registered")
	}
	if _, live := workspace.LookupBranchClaimedView(task.WorkspaceRoot); live {
		t.Fatal("settlement left the claim record")
	}
}
