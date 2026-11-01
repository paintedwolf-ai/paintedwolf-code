package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s mergeEngine) registerReconcilePaths(sessionID string, assessment PromoteAssessment) {
	if s.MergeService == nil || s.Reconcile == nil {
		return
	}
	var paths []string
	for _, c := range assessment.Conflicts {
		paths = append(paths, c.Path)
	}
	if len(paths) > 0 {
		s.Reconcile.SetMergeReconcilePaths(sessionID, paths)
	}
}

func (s mergeEngine) recordPromoteOutput(sessionID, jobID string, out *api.WorkerMergeResult) {
	if s.MergeService == nil || s.Reconcile == nil || out == nil {
		return
	}
	if rows := promotionstate.NormalizePromotePathStatusRows(out.PathStatus); len(rows) > 0 {
		s.Reconcile.RecordPromotePathStatus(sessionID, jobID, rows)
	}
	s.Reconcile.RecordOverlayPreviewSummary(sessionID, jobID, out)
	if out.Status == api.WorkerMergeStatusMerged || out.Status == api.WorkerMergeStatusAborted {
		s.Reconcile.ClearPromotePathStatus(sessionID, jobID)
	}
}

func (s mergeEngine) clearReconcilePaths(sessionID string) {
	if s.MergeService == nil || s.Reconcile == nil {
		return
	}
	s.Reconcile.ClearMergeReconcilePaths(sessionID)
}

func prepareMergeMutations(task *api.WorkerTask, assessment PromoteAssessment) ([]promoteMutation, error) {
	byPath := map[string]PromoteMergeResult{}
	for _, mr := range assessment.MergeResults {
		byPath[mr.Path] = mr
	}
	deleted := map[string]struct{}{}
	for _, path := range assessment.DeletedPaths {
		deleted[path] = struct{}{}
	}
	plans := make([]promoteMutation, 0, len(assessment.CleanPaths))
	for _, path := range assessment.CleanPaths {
		snapshot, ok := assessment.primary[path]
		if !ok {
			return nil, fmt.Errorf("promote assessment has no primary snapshot for %q", path)
		}
		plan := promoteMutation{
			path: path, original: append([]byte(nil), snapshot.content...),
			originalExists: snapshot.exists, originalMode: snapshot.mode,
		}
		if _, drop := deleted[path]; drop {
			plans = append(plans, plan)
			continue
		}
		result, ok := byPath[path]
		if !ok {
			continue
		}
		target, err := textfile.EncodeBounded(result.Content, result.Encoding,
			textfile.LimitsForRaw(promoteConflictMaxFileBytes))
		if err != nil {
			return nil, err
		}
		plan.target = target
		plan.targetExists = true
		plans = append(plans, plan)
	}
	return plans, nil
}

// notifyMergeWorktreeChanged invalidates each root changed by promotion.
func notifyMergeWorktreeChanged(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) {
	dirs := make([]string, 0, len(roots)+1)
	seen := map[string]struct{}{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		dirs = append(dirs, p)
	}
	for _, r := range roots {
		add(r.Path)
	}
	if len(dirs) == 0 && task != nil {
		add(task.WorkspacePath)
	}
	for _, dir := range dirs {
		repochange.Notify(ctx, repochange.Event{
			ProjectDir: dir,
			Kind:       repochange.WorktreeChanged,
			Source:     repochange.SourceGitHost,
		})
	}
}

func (s mergeEngine) rejectConflict(jobID string, assessment PromoteAssessment) error {
	conflictPaths := make([]string, 0, len(assessment.Conflicts))
	for _, c := range assessment.Conflicts {
		conflictPaths = append(conflictPaths, c.Path)
	}
	data := map[string]any{
		"job_id":            jobID,
		"conflict_count":    len(assessment.Conflicts),
		"paths":             conflictPaths,
		"path_status":       buildPathStatus(assessment, nil),
		"ready_resolutions": promoteRecoveryChoices(assessment),
	}
	if overlap := allOverlapJobIDs(assessment); len(overlap) > 0 {
		data["overlap_job_ids"] = overlap
	}
	return s.reject(OverlayPromoteConflictCode, data)
}

// Rejection recovery identifies choices without copying proposed source bodies.
func promoteRecoveryChoices(assessment PromoteAssessment) []map[string]any {
	ready := buildReadyResolutions(assessment)
	choices := make([]map[string]any, 0, len(ready))
	for _, item := range ready {
		choices = append(choices, map[string]any{"path": item.Path, "action": item.Action, "needs_review": item.NeedsReview, "conflict_tier": item.ConflictTier})
	}
	return choices
}

// recoverInterruptedPromote rolls back an expired promotion.
func (s mergeEngine) recoverInterruptedPromote(ctx context.Context, task *api.WorkerTask, jobID string) error {
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
func (s mergeEngine) reclaimOrphanedApply(ctx context.Context, task *api.WorkerTask, jobID string) (bool, error) {
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

func (s mergeEngine) failPromoteTransaction(tx *promoteTransaction, promote PromoteRoots, task *api.WorkerTask, revertPending func() error, cause error) error {
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

func (s mergeEngine) closeCommittedPromoteTransaction(ctx context.Context, tx *promoteTransaction, jobID string) {
	if err := tx.close(); err != nil {
		slog.WarnContext(ctx, "committed promote journal cleanup deferred", "job_id", jobID, "err", err)
	}
}

type promoteFinishInput struct {
	sessionID     string
	task          *api.WorkerTask
	jobID         string
	out           *api.WorkerMergeResult
	resolved      []string
	landed        []string
	detail        string
	claim         *mergeApplyClaim
	facts         promotionSourceFacts
	childStatuses []MergeStatusUpdate
}

func (s mergeEngine) finishPromoteState(ctx context.Context, in promoteFinishInput) error {
	fullPaths := s.jobPromotePaths(ctx, in.task)
	fullAssessment, err := s.assessMerge(ctx, in.sessionID, in.task, fullPaths)
	if err != nil {
		return err
	}
	remaining := remainingPromoteWork(fullAssessment, in.resolved)
	in.out.Applied = append([]string(nil), in.resolved...)
	fillMergeResult(in.out, remaining, promoteOutputOpts{Detail: in.detail})
	if mergeComplete(remaining) {
		return s.commitCompletedPromotion(ctx, in)
	}
	s.registerReconcilePaths(in.sessionID, remaining)
	if err := s.releaseMergeClaim(ctx, in.claim); err != nil {
		return fmt.Errorf("persist pending overlay: %w", err)
	}
	in.out.Status = api.WorkerMergeStatusPending
	return nil
}

func (s mergeEngine) commitCompletedPromotion(ctx context.Context, in promoteFinishInput) error {
	plan := obligation.Plan{}
	if s.Scans != nil && in.task != nil {
		var planErr error
		plan, planErr = s.Scans.PrepareOverlayPromotion(ctx, *in.task, in.landed, deletedPromotionPaths(in.facts.changes))
		if planErr != nil {
			return fmt.Errorf("prepare landed change: %w", planErr)
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	in.facts.stageDocuments(ctx)
	in.claim.stop()
	commitCtx, cancel := promoteCleanupContext(ctx)
	err := s.Store.CommitPromotion(commitCtx, in.jobID, in.claim.token, PromotionCommit{
		Plan: plan, Recorder: s.SourceLedger, Documents: in.facts.documentWriter(),
		Records: in.facts.records, Changes: in.facts.changes, ChildStatuses: in.childStatuses,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("commit merged overlay: %w", err)
	}
	in.facts.documents.release(ctx, true)
	s.clearReconcilePaths(in.sessionID)
	s.finalizeMergedOverlay(ctx, in.task, in.jobID, plan)
	in.out.Status = api.WorkerMergeStatusMerged
	return nil
}

func (s mergeEngine) finalizeMergedOverlay(ctx context.Context, task *api.WorkerTask, jobID string, plan obligation.Plan) {
	if s.Scans != nil && !plan.Empty() {
		if err := s.Scans.PublishObligation(ctx, plan); err != nil {
			slog.WarnContext(ctx, "scan obligation publication deferred", "landed_change_id", plan.ID, "scan_id", plan.ScanID, "err", err)
		}
	}
	if s.Coord != nil && task != nil {
		_ = s.Coord.ReleaseReservations(ctx, task.ParentSessionID, jobID)
	}
	if s.Closeout != nil && task != nil && task.DelegationID != "" {
		_ = s.Closeout.RetryCloseout(ctx, task.DelegationID)
	}
	if err := s.teardownBranch(task); err != nil {
		slog.WarnContext(ctx, "merged overlay cleanup deferred", "job_id", jobID, "err", err)
		return
	}
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	err := s.Store.ClearWorkerWorkspace(cleanupCtx, jobID)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "merged overlay metadata cleanup deferred", "job_id", jobID, "err", err)
	}
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

func promoteCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}
