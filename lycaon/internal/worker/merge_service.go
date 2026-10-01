package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// Overlay promote reject codes map to structured guidance.
const (
	OverlayPromoteNotFoundCode            = "OVERLAY_PROMOTE_NOT_FOUND"
	OverlayPromoteSessionMismatchCode     = "OVERLAY_PROMOTE_SESSION_MISMATCH"
	OverlayPromoteNotPendingCode          = "OVERLAY_PROMOTE_NOT_PENDING"
	OverlayPromoteNoPathsCode             = "OVERLAY_PROMOTE_NO_PATHS"
	OverlayPromoteConflictCode            = "OVERLAY_PROMOTE_CONFLICT"
	OverlayPromoteResolutionsRequiredCode = "OVERLAY_PROMOTE_RESOLUTIONS_REQUIRED"
	OverlayPromoteSyntaxUnhealthyCode     = "OVERLAY_PROMOTE_SYNTAX_UNHEALTHY"
)

// MergeQueue loads worker jobs and materializes their branches.
type MergeQueue interface {
	Get(jobID string) (*api.WorkerTask, bool)
	WorkerBranches
}

// MergeStore persists leased promotion state.
type MergeStore interface {
	BeginMergeApply(ctx context.Context, jobID string) (claimToken string, ok bool, err error)
	RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error)
	ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error
	ReclaimExpiredMergeApply(ctx context.Context, jobID string) (claimToken string, ok bool, err error)
	ListMergeApplying(ctx context.Context) ([]string, error)
	SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error
	SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error
	CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error
	ClearWorkerWorkspace(ctx context.Context, jobID string) error
}

type MergeStatusUpdate struct {
	JobID  string
	Status api.WorkerMergeStatus
}

type PromotionCommit struct {
	Plan     obligation.Plan
	Recorder sourceledger.BatchRecorder
	// Documents writes staged editor imports before Records, whose text states reference them.
	Documents     PromotedDocumentWriter
	Records       []sourceledger.RecordInput
	Changes       []sourcefeed.Change
	ChildStatuses []MergeStatusUpdate
}

// PromotedDocumentWriter joins the promotion transaction with staged editor document imports.
type PromotedDocumentWriter interface {
	CommitTx(ctx context.Context, tx *sql.Tx) error
}

// MergeSessionLister supplies pending and live overlay sets.
type MergeSessionLister interface {
	ListPendingOverlayPromote(ctx context.Context, sessionID string) ([]api.WorkerTask, error)
	ListLiveOverlaysForSession(ctx context.Context, sessionID string) ([]api.WorkerTask, error)
}

// WorkerCoordinationCleanup releases landed worker coordination state.
type WorkerCoordinationCleanup interface {
	ReleaseWorkerReservations(ctx context.Context, parentSessionID, jobID string) error
}

// DelegationCloseoutRetry re-evaluates closeout after promotion.
type DelegationCloseoutRetry interface {
	RetryCloseout(ctx context.Context, delegationID string) error
}

// OverlayScanHook runs promotion scan obligations.
type OverlayScanHook interface {
	PrepareOverlayPromotion(ctx context.Context, task api.WorkerTask, appliedPaths, deletedPaths []string) (obligation.Plan, error)
	PublishObligation(ctx context.Context, plan obligation.Plan) error
}

// OverlayDocumentSynchronizer holds open editor documents across a promotion so
// their imports commit atomically with it.
type OverlayDocumentSynchronizer interface {
	HoldPromotedDocuments(ctx context.Context, p *project.Project, refs []editordoc.PathRef) (editordoc.PromotedDocumentHold, error)
}

// MergeService applies coordinator merge tools to worker branches.
type MergeService struct {
	Queue        MergeQueue
	Store        MergeStore
	Reports      ChangeReportDeps
	Evidence     SourceEvidenceContext
	Workspace    WorkerWorkspaceManager
	Reject       *guidance.StaticRejectFormatter
	Sessions     MergeSessionLister
	Reconcile    session.MergeReconcileRegistrar
	Coord        WorkerCoordinationCleanup
	Closeout     DelegationCloseoutRetry
	Projects     ProjectStore
	Scans        OverlayScanHook
	SourceLedger sourceledger.PromoteRecorder
	Documents    OverlayDocumentSynchronizer
	DataDir      string
}

// PreviewForSession assesses one overlay without changing state.
func (s *MergeService) PreviewForSession(ctx context.Context, sessionID, jobID, detail string, paths []string) (api.WorkerMergeResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if s == nil || s.Queue == nil {
		return api.WorkerMergeResult{}, fmt.Errorf("overlay promote service not configured")
	}
	task, ok := s.Queue.Get(jobID)
	if !ok || task == nil {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": jobID})
	}
	if strings.TrimSpace(task.ParentSessionID) != sessionID {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteSessionMismatchCode, map[string]any{"job_id": jobID})
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotPendingCode, map[string]any{"job_id": jobID, "reason": "no isolated branch"})
	}
	if task.MergeStatus != api.WorkerMergeStatusPending {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotPendingCode, map[string]any{
			"job_id":       jobID,
			"merge_status": string(task.MergeStatus),
		})
	}
	lease, err := s.holdBranch(ctx, jobID)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	defer lease.Release()
	out := api.WorkerMergeResult{
		JobID:     jobID,
		Mode:      "preview",
		AgentType: task.AgentType,
	}
	opts := promoteOutputOpts{Detail: detail}
	roots := s.taskRootRefs(ctx, task)
	changed, err := session.InspectOverlayChanges(ctx, task, roots)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	mergePaths := normalizeMergePaths(ctx, paths, *task, roots)
	if len(paths) > 0 {
		validated, err := validatePromotePaths(task, roots, s.jobPromotePaths(ctx, task), mergePaths)
		if err != nil {
			return api.WorkerMergeResult{}, err
		}
		mergePaths = validated
	}
	if len(mergePaths) == 0 {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNoPathsCode, map[string]any{"job_id": jobID, "remaining_overlay_paths": changed})
	}
	out.Paths = mergePaths
	assessment, err := s.assessMerge(ctx, sessionID, task, mergePaths)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	fillMergeResult(&out, assessment, opts)
	s.attachPromoteSpill(s.hostDataDirFor(task), jobID, &out, assessment)
	s.registerReconcilePaths(sessionID, assessment)
	enrichMergeChangedPaths(&out, *task, s.reports(), ctx)
	EnrichOverlayIntent(&out, task)
	out.Status = api.WorkerMergeStatusPending
	s.attachSourceEvidence(ctx, task, &out)
	s.recordPromoteOutput(sessionID, jobID, &out)
	return out, nil
}

func (s *MergeService) overlayMessages(ctx context.Context, task *api.WorkerTask) []api.Message {
	if s == nil || task == nil || s.Reports.Messages == nil {
		return nil
	}
	childID := strings.TrimSpace(task.ChildSessionID)
	if childID == "" {
		return nil
	}
	msgs, err := s.Reports.Messages(ctx, childID)
	if err != nil {
		return nil
	}
	return msgs
}

func (s *MergeService) attachSourceEvidence(ctx context.Context, task *api.WorkerTask, out *api.WorkerMergeResult) {
	if s == nil || out == nil || task == nil {
		return
	}
	proof := session.OverlaySourceProof(ctx, task, s.overlayMessages(ctx, task), s.Evidence.command(ctx, task), s.Evidence.revision(ctx, task))
	if s.Reports.SourceRuns != nil {
		runs, err := s.Reports.SourceRuns(ctx, task.ChildSessionID)
		if err != nil {
			out.SourceEvidence = &api.WorkerSourceEvidence{Kind: "source_validation", Status: workercompletion.EvidenceStatusUnmet, Reason: "Validation evidence could not be loaded."}
			return
		}
		proof = proof.WithSourceRuns(runs)
	}
	status, reason, refs := workercompletion.SourceEvidenceView(proof)
	ev := &api.WorkerSourceEvidence{
		Kind:             "source_validation",
		Status:           status,
		SourceRevision:   proof.SourceRevision,
		SourceRootDigest: proof.SourceRootDigest,
	}
	if status != workercompletion.EvidenceStatusSatisfied {
		ev.Status = workercompletion.EvidenceStatusUnmet
		ev.Reason = reason
	} else {
		ev.EvidenceRefs = refs
	}
	out.SourceEvidence = ev
}

// recoverInterruptedPromote rolls back an expired promotion.
func (s *MergeService) recoverInterruptedPromote(ctx context.Context, task *api.WorkerTask, jobID string) error {
	reclaimed, err := s.reclaimOrphanedApply(ctx, task, jobID)
	if err != nil {
		return err
	}
	if !reclaimed {
		return s.reject(OverlayPromoteNotPendingCode, map[string]any{
			"job_id": jobID,
			"reason": "another promote is applying this overlay",
		})
	}
	return nil
}

// reclaimOrphanedApply restores one expired promotion.
func (s *MergeService) reclaimOrphanedApply(ctx context.Context, task *api.WorkerTask, jobID string) (bool, error) {
	if s.Store == nil {
		return false, fmt.Errorf("recover interrupted promote: merge store not configured")
	}
	token, ok, err := s.Store.ReclaimExpiredMergeApply(ctx, jobID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	roots := s.taskRootRefs(ctx, task)
	promote := PromoteRootsForTask(task, roots)
	found, err := recoverPromoteTransaction(s.hostDataDirFor(task), jobID, promote, task)
	if err != nil {
		return true, fmt.Errorf("recover interrupted promote: %w", err)
	}
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	err = s.Store.ReleaseMergeApply(cleanupCtx, jobID, token)
	cancel()
	if err != nil {
		return true, fmt.Errorf("recover interrupted promote status: %w", err)
	}
	if found {
		if err := removePromoteTransaction(promoteTransactionPath(s.hostDataDirFor(task), jobID)); err != nil {
			slog.WarnContext(ctx, "recovered promote journal cleanup deferred", "job_id", jobID, "err", err)
		}
	}
	return true, nil
}

// RecoverOrphanedMergeApplies repairs expired promotion leases.
func (s *MergeService) RecoverOrphanedMergeApplies(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Queue == nil {
		return fmt.Errorf("merge apply sweep requires queue and store")
	}
	for {
		ids, err := s.Store.ListMergeApplying(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			task, ok := s.Queue.Get(id)
			if !ok || task == nil || task.MergeStatus != api.WorkerMergeStatusApplying {
				continue
			}
			lock, lockErr := acquirePromoteFileLock(ctx, s.hostDataDirFor(task))
			if lockErr != nil {
				return lockErr
			}
			if _, err := s.reclaimOrphanedApply(ctx, task, id); err != nil {
				slog.WarnContext(ctx, "orphaned merge apply repair deferred", "job_id", id, "err", err)
			}
			lock.release()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mergeApplyLease / 3):
		}
	}
}

func (s *MergeService) failPromoteTransaction(tx *promoteTransaction, promote PromoteRoots, task *api.WorkerTask, revertPending func() error, cause error) error {
	var unstarted *promoteTransactionUnstartedError
	if errors.As(cause, &unstarted) {
		return errors.Join(cause, tx.close(), revertPending())
	}
	rollbackErr := tx.rollback(promote, task)
	if rollbackErr != nil {
		return errors.Join(cause, rollbackErr)
	}
	closeErr := tx.close()
	return errors.Join(cause, closeErr, revertPending())
}

func (s *MergeService) closeCommittedPromoteTransaction(ctx context.Context, tx *promoteTransaction, jobID string) {
	if err := tx.close(); err != nil {
		slog.WarnContext(ctx, "committed promote journal cleanup deferred", "job_id", jobID, "err", err)
	}
}

// mergeApplyClaim holds one promotion lease.
type mergeApplyClaim struct {
	jobID         string
	token         string
	cancel        context.CancelCauseFunc
	heartbeatStop context.CancelFunc
	heartbeatDone chan struct{}
	stopOnce      sync.Once
}

var errMergeApplyClaimLost = errors.New("merge apply claim lost")

var mergeApplyHeartbeatInterval = mergeApplyLease / 3

func (s *MergeService) beginMergeClaim(ctx context.Context, jobID string) (*mergeApplyClaim, context.Context, error) {
	if s == nil || s.Store == nil {
		return nil, nil, fmt.Errorf("overlay promote requires a configured merge store")
	}
	token, claimed, err := s.Store.BeginMergeApply(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	if !claimed {
		return nil, nil, s.reject(OverlayPromoteNotPendingCode, map[string]any{"job_id": jobID})
	}
	claimCtx, cancel := context.WithCancelCause(ctx)
	claim := &mergeApplyClaim{jobID: jobID, token: token, cancel: cancel}
	s.startMergeHeartbeat(ctx, claim)
	return claim, claimCtx, nil
}

// startMergeHeartbeat renews the apply lease for the duration of the apply.
func (s *MergeService) startMergeHeartbeat(ctx context.Context, claim *mergeApplyClaim) {
	hbCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	claim.heartbeatStop = cancel
	claim.heartbeatDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(mergeApplyHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				renewed, err := s.Store.RenewMergeApply(hbCtx, claim.jobID, claim.token)
				if err != nil || !renewed {
					slog.WarnContext(hbCtx, "merge apply lease renewal lost", "job_id", claim.jobID, "err", err)
					claim.cancel(errMergeApplyClaimLost)
					return
				}
			}
		}
	}()
}

func (claim *mergeApplyClaim) stop() {
	if claim == nil {
		return
	}
	claim.stopOnce.Do(func() {
		claim.heartbeatStop()
		<-claim.heartbeatDone
		claim.cancel(nil)
	})
}

// releaseMergeClaim returns the branch to pending under the claim.
func (s *MergeService) releaseMergeClaim(ctx context.Context, claim *mergeApplyClaim) error {
	claim.stop()
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	defer cancel()
	return s.Store.ReleaseMergeApply(cleanupCtx, claim.jobID, claim.token)
}

func (s *MergeService) finishPromoteState(
	ctx context.Context,
	sessionID string,
	task *api.WorkerTask,
	jobID string,
	out *api.WorkerMergeResult,
	resolved, landed []string,
	detail string,
	claim *mergeApplyClaim,
	facts promotionSourceFacts,
	childStatuses []MergeStatusUpdate,
) error {
	fullPaths := s.jobPromotePaths(ctx, task)
	fullAssessment, err := s.assessMerge(ctx, sessionID, task, fullPaths)
	if err != nil {
		return err
	}
	remaining := remainingPromoteWork(fullAssessment, resolved)
	out.Applied = append([]string(nil), resolved...)
	fillMergeResult(out, remaining, promoteOutputOpts{Detail: detail})
	if mergeComplete(remaining) {
		// Clean no-ops produce a terminal merge.
		plan := obligation.Plan{}
		if s.Scans != nil && task != nil {
			var planErr error
			plan, planErr = s.Scans.PrepareOverlayPromotion(ctx, *task, landed, deletedPromotionPaths(facts.changes))
			if planErr != nil {
				return fmt.Errorf("prepare landed change: %w", planErr)
			}
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		facts.stageDocuments(ctx)
		claim.stop()
		commitCtx, cancel := promoteCleanupContext(ctx)
		err := s.Store.CommitPromotion(commitCtx, jobID, claim.token, PromotionCommit{
			Plan: plan, Recorder: s.SourceLedger, Documents: facts.documentWriter(),
			Records: facts.records, Changes: facts.changes, ChildStatuses: childStatuses,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("commit merged overlay: %w", err)
		}
		facts.documents.release(ctx, true)
		s.clearReconcilePaths(sessionID)
		if s.Scans != nil && !plan.Empty() {
			if err := s.Scans.PublishObligation(ctx, plan); err != nil {
				slog.WarnContext(ctx, "scan obligation publication deferred", "landed_change_id", plan.ID, "scan_id", plan.ScanID, "err", err)
			}
		}
		if s.Coord != nil && task != nil {
			_ = s.Coord.ReleaseWorkerReservations(ctx, task.ParentSessionID, jobID)
		}
		if s.Closeout != nil && task != nil && task.DelegationID != "" {
			_ = s.Closeout.RetryCloseout(ctx, task.DelegationID)
		}
		out.Status = api.WorkerMergeStatusMerged
		if err := s.teardownBranch(task); err != nil {
			slog.WarnContext(ctx, "merged overlay cleanup deferred", "job_id", jobID, "err", err)
		} else {
			cleanupCtx, cancel := promoteCleanupContext(ctx)
			err := s.Store.ClearWorkerWorkspace(cleanupCtx, jobID)
			cancel()
			if err != nil {
				slog.WarnContext(ctx, "merged overlay metadata cleanup deferred", "job_id", jobID, "err", err)
			}
		}
		return nil
	}
	s.registerReconcilePaths(sessionID, remaining)
	if err := s.releaseMergeClaim(ctx, claim); err != nil {
		return fmt.Errorf("persist pending overlay: %w", err)
	}
	out.Status = api.WorkerMergeStatusPending
	return nil
}

func deletedPromotionPaths(changes []sourcefeed.Change) []string {
	deleted := make([]string, 0)
	for _, change := range changes {
		if change.Op == api.SourceChangeOpDelete {
			deleted = append(deleted, change.Path)
		}
	}
	return deleted
}

func (s *MergeService) assessMerge(ctx context.Context, sessionID string, task *api.WorkerTask, mergePaths []string) (PromoteAssessment, error) {
	if task == nil {
		return PromoteAssessment{}, nil
	}
	return AssessPromotePaths3Way(ctx, s, sessionID, task, mergePaths)
}

func (s *MergeService) teardownBranch(task *api.WorkerTask) error {
	if s == nil || s.Workspace == nil || task == nil {
		return nil
	}
	return s.Workspace.DestroyWorkerWorkspace(&workspace.Binding{
		ID:   task.ID,
		Root: task.WorkspaceRoot,
	})
}

func promoteCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *MergeService) hostDataDirFor(task *api.WorkerTask) string {
	if task == nil {
		return ""
	}
	dataDir := ""
	if s != nil {
		dataDir = s.DataDir
	}
	if id := strings.TrimSpace(task.ProjectID); id != "" {
		dir, err := project.EnsureHostDataDir(dataDir, id)
		if err == nil {
			return dir
		}
	}
	if dir := project.PathKeyedHostDataDir(dataDir, task.WorkspacePath); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		return dir
	}
	return strings.TrimSpace(task.WorkspacePath)
}

func (s *MergeService) reports() ChangeReportDeps {
	if s == nil {
		return ChangeReportDeps{}
	}
	return s.Reports
}

func (s *MergeService) reject(code string, data map[string]any) error {
	var formatter *guidance.StaticRejectFormatter
	if s != nil {
		formatter = s.Reject
	}
	return tools.FormatDecisionReject(code, data, formatter)
}

func normalizeMergePaths(ctx context.Context, paths []string, task api.WorkerTask, roots []projectroot.RootRef) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range paths {
		add(p)
	}
	if len(out) > 0 {
		return out
	}
	for _, p := range session.OverlayWorkspaceChangedPaths(ctx, &task, roots) {
		add(p)
	}
	return out
}

// holdBranch leases the overlay's branch tree for a merge read, rebuilding a
// reclaimed tree. A tree that cannot be rebuilt rejects as not pending.
func (s *MergeService) holdBranch(ctx context.Context, jobID string) (*BranchLease, error) {
	_, lease, err := s.Queue.EnsureWorkerBranch(ctx, jobID)
	if err != nil {
		if errors.Is(err, ErrWorkerBranchUnavailable) {
			return nil, s.reject(OverlayPromoteNotPendingCode, map[string]any{
				"job_id": jobID, "reason": "overlay workspace unavailable",
			})
		}
		return nil, err
	}
	return lease, nil
}

func (s *MergeService) taskRootRefs(ctx context.Context, task *api.WorkerTask) []projectroot.RootRef {
	var projects ProjectStore
	if s != nil {
		projects = s.Projects
	}
	return TaskRootRefs(ctx, task, projects)
}
