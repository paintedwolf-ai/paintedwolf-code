package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/idset"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *MergeService) registerReconcilePaths(sessionID string, assessment PromoteAssessment) {
	if s == nil || s.Reconcile == nil {
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

func (s *MergeService) recordPromoteOutput(sessionID, jobID string, out *api.WorkerMergeResult) {
	if s == nil || s.Reconcile == nil || out == nil {
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

func (s *MergeService) clearReconcilePaths(sessionID string) {
	if s == nil || s.Reconcile == nil {
		return
	}
	s.Reconcile.ClearMergeReconcilePaths(sessionID)
}

func prepareMergeMutations(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef, assessment PromoteAssessment) ([]promoteMutation, error) {
	if err := session.RevalidateTaskCheckpointPreview(ctx, task, roots); err != nil {
		return nil, err
	}
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

func (s *MergeService) rejectConflict(jobID string, assessment PromoteAssessment) error {
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

func assessCheckpointTask(ctx context.Context, svc *MergeService, sessionID string, task *api.WorkerTask, mergePaths []string) (PromoteAssessment, bool, error) {
	var roots []projectroot.RootRef
	if svc != nil {
		roots = svc.taskRootRefs(ctx, task)
	}
	store, manifest, promoter, ok, err := session.NewTaskCheckpointPromoter(task, roots)
	if err != nil || !ok {
		return PromoteAssessment{}, ok, err
	}
	cpPreview, err := taskCheckpointPreview(ctx, svc, task, roots, manifest, promoter)
	if err != nil {
		return PromoteAssessment{}, true, err
	}

	wanted := make(map[string]struct{}, len(mergePaths))
	for _, p := range mergePaths {
		wanted[filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))] = struct{}{}
	}
	promote := PromoteRootsForTask(task, roots)
	out := PromoteAssessment{checkpointID: manifest.CheckpointID, primary: make(map[string]promotePrimarySnapshot, len(mergePaths))}
	for _, rel := range mergePaths {
		if primaryAbs, _, fErr := promote.fileAbs(task, rel); fErr == nil {
			b, ex, mode, _ := readOptionalPromoteFile(primaryAbs)
			out.primary[rel] = promotePrimarySnapshot{content: b, exists: ex, mode: mode}
		}
	}
	seen := make(map[string]bool, len(mergePaths))
	for _, ch := range cpPreview.PlannedChanges {
		rel := session.QualifyCheckpointPath(ch.Path, roots)
		if rel == "" {
			continue
		}
		if len(wanted) > 0 {
			if _, match := wanted[rel]; !match {
				continue
			}
		}
		seen[rel] = true
		out.CleanPaths = append(out.CleanPaths, rel)
		switch ch.Action {
		case checkpoint.ActionDelete:
			out.DeletedPaths = append(out.DeletedPaths, rel)
		case checkpoint.ActionWriteRegular, checkpoint.ActionReplaceDirWithRegular:
			out.MergeResults = append(out.MergeResults, PromoteMergeResult{Path: rel, Content: string(ch.MergedBytes), Encoding: textfile.UTF8})
		default:
		}
	}
	for _, conf := range cpPreview.Conflicts {
		rel := session.QualifyCheckpointPath(conf.Path, roots)
		if rel == "" {
			continue
		}
		if len(wanted) > 0 {
			if _, match := wanted[rel]; !match {
				continue
			}
		}
		seen[rel] = true
		pc := convertCheckpointConflict(store, out.primary[rel], conf, rel)
		if svc != nil {
			pc.OverlapJobIDs = svc.overlapJobsForPath(ctx, sessionID, rel, task.ID)
			out.OverlapJobIDs = idset.UnionSorted(out.OverlapJobIDs, pc.OverlapJobIDs)
		}
		out.Conflicts = append(out.Conflicts, pc)
	}
	for _, raw := range mergePaths {
		rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if rel != "" && !seen[rel] {
			seen[rel] = true
			out.CleanPaths = append(out.CleanPaths, rel)
		}
	}
	enrichAssessmentPromoteOrders(ctx, svc, sessionID, task, &out)
	return out, true, nil
}

func convertCheckpointConflict(store *checkpoint.Store, prim promotePrimarySnapshot, conf checkpoint.PromotionConflict, rel string) PromoteConflict {
	var baseStr, branchStr string
	if conf.Base.Kind == "regular" && conf.Base.SHA256 != "" {
		if b, err := store.ReadBlob(conf.Base.SHA256); err == nil {
			baseStr = string(b)
		}
	}
	if conf.WorkerFinal.SHA256 != "" {
		if b, err := store.ReadBlob(conf.WorkerFinal.SHA256); err == nil {
			branchStr = string(b)
		}
	}
	return PromoteConflict{
		Path: rel, Reason: api.WorkerPromoteReasonThreeWayUnresolved,
		Base: baseStr, Primary: string(prim.content), Branch: branchStr,
		Summary: []string{fmt.Sprintf("%s: %s", conf.Kind, conf.Message)},
	}
}

// taskCheckpointPreview previews what promoting a task's checkpoint would do. A stacked checkpoint
// is previewed as its composed chain: its own bases are its parent's content, not the target's.
func taskCheckpointPreview(ctx context.Context, svc *MergeService, task *api.WorkerTask, roots []projectroot.RootRef, manifest checkpoint.Manifest, promoter *checkpoint.Promoter) (*checkpoint.PromotionPreview, error) {
	if len(manifest.ParentCheckpoints) > 0 {
		plan, _, err := svc.stackedPromotionPlan(ctx, task, roots, manifest, promoter)
		if err != nil {
			return nil, err
		}
		return plan.Preview, nil
	}
	preview, err := promoter.Preview(ctx, checkpoint.PreviewRequest{
		CheckpointID: manifest.CheckpointID, ExpectedWorkerID: manifest.WorkerID, ExpectedProjectID: task.ProjectID,
	})
	if err != nil {
		return nil, err
	}
	session.MaybeBindTaskCheckpointPreview(task.ID, preview)
	return preview, nil
}

func (s *MergeService) stackedPromotionRequest(task *api.WorkerTask, roots []projectroot.RootRef, manifest checkpoint.Manifest, promoter *checkpoint.Promoter, resolvedPaths []string) (*recovery.Coordinator, recovery.StackedPromotionRequest, error) {
	if s == nil || s.Workspace == nil {
		return nil, recovery.StackedPromotionRequest{}, fmt.Errorf("stacked promotion requires the recovery coordinator")
	}
	coordinator, err := s.Workspace.CheckpointCoordinator()
	if err != nil {
		return nil, recovery.StackedPromotionRequest{}, err
	}
	targetRoots, err := session.TargetRootsForTaskCheckpoint(task, roots, manifest)
	if err != nil {
		return nil, recovery.StackedPromotionRequest{}, err
	}
	resolutions := session.CheckpointKeepTargetResolutions(task, manifest, roots, resolvedPaths)
	return coordinator, recovery.StackedPromotionRequest{
		ChildCheckpointID: manifest.CheckpointID,
		TargetRoots:       targetRoots,
		Promoter:          promoter,
		Resolutions:       resolutions,
		ImportGitRefs:     true,
	}, nil
}

func (s *MergeService) stackedPromotionPlan(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef, manifest checkpoint.Manifest, promoter *checkpoint.Promoter) (*recovery.StackedPromotionPlan, *recovery.Coordinator, error) {
	coordinator, request, err := s.stackedPromotionRequest(task, roots, manifest, promoter, nil)
	if err != nil {
		return nil, nil, err
	}
	plan, err := coordinator.ResolveStackedPromotionPlan(ctx, request)
	return plan, coordinator, err
}

// applyTaskCheckpoint joins a task's checkpoint effects to its promotion transaction, stacked or not.
func applyTaskCheckpoint(ctx context.Context, s *MergeService, tx *promoteTransaction, task *api.WorkerTask, resolvedPaths []string) error {
	if task == nil {
		return nil
	}
	var roots []projectroot.RootRef
	if s != nil {
		roots = s.taskRootRefs(ctx, task)
	}
	_, manifest, promoter, ok, err := session.NewTaskCheckpointPromoter(task, roots)
	if err != nil {
		return errors.Join(err, tx.txn.Rollback(context.WithoutCancel(ctx)))
	}
	if !ok || len(manifest.ParentCheckpoints) == 0 {
		return session.ApplyTaskCheckpoint(ctx, tx.txn, task, roots, resolvedPaths...)
	}
	coordinator, request, err := s.stackedPromotionRequest(task, roots, manifest, promoter, resolvedPaths)
	if err != nil {
		return errors.Join(err, tx.txn.Rollback(context.WithoutCancel(ctx)))
	}
	request.Transaction = tx.txn
	if _, err := coordinator.PromoteStackedCheckpoint(ctx, request); err != nil {
		return err
	}
	tx.afterCommit = func() error { return coordinator.SettleStackedPromotion(manifest.CheckpointID) }
	return nil
}
