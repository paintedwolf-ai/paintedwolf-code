package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromoteOverlay implements session.OverlayPromoter for the worker merge service.
func (s *MergeService) PromoteOverlay(ctx context.Context, sessionID, overlayID string, in api.PromoteOverlayInput) (api.WorkerMergeResult, error) {
	overlayID = strings.TrimSpace(overlayID)
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.Queue == nil {
		return api.WorkerMergeResult{}, fmt.Errorf("overlay promote service not configured")
	}
	if overlayID == "" {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": overlayID})
	}
	lockTask, ok := s.Queue.Get(overlayID)
	if !ok || lockTask == nil {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": overlayID})
	}
	lock, err := acquirePromoteFileLock(ctx, s.hostDataDirFor(lockTask))
	if err != nil {
		return api.WorkerMergeResult{}, fmt.Errorf("acquire promote lock: %w", err)
	}
	defer lock.release()
	work, err := s.resolvePromoteWork(ctx, sessionID, overlayID)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	defer work.lease.Release()
	task, roots, paths := work.task, work.roots, work.paths

	allowed, err := allowedPromotePathSet(task, roots, paths)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	callerResolutions := expandResolutionDropFolders(in.Resolutions, allowed)

	callerPaths := map[string]struct{}{}
	for _, r := range callerResolutions {
		if p := strings.TrimSpace(r.Path); p != "" {
			callerPaths[p] = struct{}{}
		}
	}
	var mergePaths []string
	for _, p := range paths {
		if _, ok := callerPaths[p]; !ok {
			mergePaths = append(mergePaths, p)
		}
	}

	assessment := PromoteAssessment{}
	if len(mergePaths) > 0 {
		var aerr error
		assessment, aerr = s.assessMerge(ctx, sessionID, task, mergePaths)
		if aerr != nil {
			return api.WorkerMergeResult{}, aerr
		}
	}
	autoResolutions := readyResolutionsToPromote(buildReadyResolutions(assessment), true)
	resolutions := unionPromoteResolutions(autoResolutions, callerResolutions)
	resolutionByPath := make(map[string]api.WorkerPromoteResolution, len(resolutions))
	for _, resolution := range resolutions {
		if path := strings.TrimSpace(resolution.Path); path != "" {
			resolutionByPath[path] = resolution
		}
	}
	for _, conflict := range assessment.Conflicts {
		if _, resolved := resolutionByPath[conflict.Path]; resolved {
			continue
		}
		out := api.WorkerMergeResult{JobID: overlayID, Mode: "merge", AgentType: task.AgentType, Paths: paths}
		fillMergeResult(&out, assessment, promoteOutputOpts{Detail: in.Detail})
		s.attachPromoteSpill(s.hostDataDirFor(task), overlayID, &out, assessment)
		s.registerReconcilePaths(sessionID, assessment)
		s.recordPromoteOutput(sessionID, overlayID, &out)
		return out, s.rejectConflict(overlayID, assessment)
	}

	validated, err := validatePromoteResolutionPaths(task, roots, paths, resolutions)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	resolutionPlans, err := preparePromoteResolutions(task, roots, validated, resolutionByPath)
	if err != nil {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteResolutionsRequiredCode, map[string]any{
			"job_id": overlayID,
			"reason": err.Error(),
		})
	}
	mergePlans, err := prepareMergeMutations(task, assessment)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	plans := append(append([]promoteMutation(nil), mergePlans...), resolutionPlans...)
	if issue := validatePromoteSyntax(ctx, plans); issue != nil {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteSyntaxUnhealthyCode, issue.rejectData(overlayID))
	}
	applied := unionPromotePaths(assessment.CleanPaths, validated)

	targets, err := s.snapshotPromoteTargets(task, roots, paths)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	if len(applied) == 0 {
		changed, err := session.InspectOverlayChanges(ctx, task, roots)
		if err != nil {
			return api.WorkerMergeResult{}, err
		}
		if len(changed) > 0 {
			return api.WorkerMergeResult{}, s.reject(OverlayPromoteNoPathsCode, map[string]any{"job_id": overlayID, "remaining_overlay_paths": changed})
		}
		out, err := s.finalizeEmptyPromote(ctx, sessionID, task, overlayID, in.Detail)
		if err != nil {
			return out, err
		}
		return out, nil
	}

	// The hold outlives any rollback of landed bytes, so observation never sees them first.
	documents, err := s.holdPromotedDocuments(ctx, task, targets, applied)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	defer documents.release(ctx, false)
	claim, claimCtx, err := s.beginMergeClaim(ctx, overlayID)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	revertPending := func() error { return s.releaseMergeClaim(ctx, claim) }
	if s.Reconcile != nil {
		s.Reconcile.RecordPromotedPrimaryPaths(ctx, sessionID, promoteMutationPaths(plans))
	}
	promote := PromoteRootsForTask(task, roots)
	tx, err := beginPromoteTransaction(s.hostDataDirFor(task), overlayID, plans)
	if err != nil {
		return api.WorkerMergeResult{}, errors.Join(err, revertPending())
	}
	if err := tx.apply(claimCtx, promote, task); err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, task, revertPending, err)
	}
	if len(plans) > 0 {
		notifyMergeWorktreeChanged(ctx, task, roots)
	}
	facts, err := s.promotedSourceFacts(
		claimCtx, sessionID, in.ToolCallID, sourceworkspace.ID(task.ProjectID, roots),
		in.UserTurn, task, targets, applied, documents,
	)
	if err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, task, revertPending, err)
	}
	rebased, childStatuses, err := s.assessChildRebases(claimCtx, sessionID, overlayID)
	if err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, task, revertPending, err)
	}
	out := api.WorkerMergeResult{JobID: overlayID, Mode: "merge", AgentType: task.AgentType, Paths: paths, Rebased: rebased}
	if err := s.finishPromoteState(claimCtx, sessionID, task, overlayID, &out, applied, promoteMutationPaths(plans), in.Detail, claim, facts, childStatuses); err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, task, revertPending, err)
	}
	if out.Status != api.WorkerMergeStatusMerged {
		err := fmt.Errorf("promote plan incomplete after apply")
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, task, revertPending, err)
	}
	s.closeCommittedPromoteTransaction(ctx, tx, overlayID)
	if len(facts.files) > 0 {
		out.OverlayPromotion = &api.OverlayPromotion{Files: facts.files}
	}
	s.recordPromoteOutput(sessionID, overlayID, &out)
	return out, nil
}

type promoteWork struct {
	task  *api.WorkerTask
	roots []projectroot.RootRef
	paths []string
	// lease keeps the branch tree on disk until the promote is over.
	lease *BranchLease
}

func (s *MergeService) resolvePromoteWork(ctx context.Context, sessionID, overlayID string) (promoteWork, error) {
	task, ok := s.Queue.Get(overlayID)
	if !ok || task == nil {
		return promoteWork{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": overlayID})
	}
	if strings.TrimSpace(task.ParentSessionID) != sessionID {
		return promoteWork{}, s.reject(OverlayPromoteSessionMismatchCode, map[string]any{"job_id": overlayID})
	}
	if task.MergeStatus == api.WorkerMergeStatusApplying {
		if err := s.recoverInterruptedPromote(ctx, task, overlayID); err != nil {
			return promoteWork{}, err
		}
		task.MergeStatus = api.WorkerMergeStatusPending
	}
	if task.MergeStatus != api.WorkerMergeStatusPending {
		return promoteWork{}, s.reject(OverlayPromoteNotPendingCode, map[string]any{
			"overlay_id": overlayID, "merge_status": string(task.MergeStatus),
		})
	}
	lease, err := s.holdBranch(ctx, overlayID)
	if err != nil {
		return promoteWork{}, err
	}
	roots := s.taskRootRefs(ctx, task)
	return promoteWork{
		task: task, roots: roots, paths: s.jobPromotePaths(ctx, task), lease: lease,
	}, nil
}

// expandResolutionDropFolders expands folder drops into candidate file drops.
func expandResolutionDropFolders(resolutions []api.WorkerPromoteResolution, allowed map[string]struct{}) []api.WorkerPromoteResolution {
	var out []api.WorkerPromoteResolution
	for _, res := range resolutions {
		p := strings.TrimSpace(res.Path)
		if res.Action != api.WorkerPromoteResolutionActionDrop || p == "" {
			out = append(out, res)
			continue
		}
		if _, isFile := allowed[p]; isFile {
			out = append(out, res)
			continue
		}
		prefix := strings.TrimSuffix(p, "/") + "/"
		for f := range allowed {
			if strings.HasPrefix(f, prefix) {
				out = append(out, api.WorkerPromoteResolution{Path: f, Action: api.WorkerPromoteResolutionActionDrop})
			}
		}
	}
	return out
}

// finalizeEmptyPromote closes an overlay with no changes.
func (s *MergeService) finalizeEmptyPromote(ctx context.Context, sessionID string, task *api.WorkerTask, jobID, detail string) (api.WorkerMergeResult, error) {
	out := api.WorkerMergeResult{JobID: jobID, Mode: "merge", AgentType: task.AgentType}
	claim, claimCtx, err := s.beginMergeClaim(ctx, jobID)
	if err != nil {
		return out, err
	}
	rebased, childStatuses, err := s.assessChildRebases(claimCtx, sessionID, jobID)
	if err != nil {
		return out, errors.Join(err, s.releaseMergeClaim(ctx, claim))
	}
	out.Rebased = rebased
	if err := s.finishPromoteState(claimCtx, sessionID, task, jobID, &out, nil, nil, detail, claim, promotionSourceFacts{}, childStatuses); err != nil {
		return out, errors.Join(err, s.releaseMergeClaim(ctx, claim))
	}
	s.recordPromoteOutput(sessionID, jobID, &out)
	return out, nil
}

// RejectOverlay closes an overlay and orphans its stacked children.
func (s *MergeService) RejectOverlay(ctx context.Context, sessionID, overlayID, reason string) (api.OverlayRejectOutcome, error) {
	overlayID = strings.TrimSpace(overlayID)
	if overlayID == "" {
		return api.OverlayRejectOutcome{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": overlayID})
	}
	if s == nil || s.Queue == nil {
		return api.OverlayRejectOutcome{}, fmt.Errorf("merge service not configured")
	}
	task, ok := s.Queue.Get(overlayID)
	if !ok || task == nil {
		return api.OverlayRejectOutcome{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": overlayID})
	}
	lock, err := acquirePromoteFileLock(ctx, s.hostDataDirFor(task))
	if err != nil {
		return api.OverlayRejectOutcome{}, fmt.Errorf("acquire promote lock: %w", err)
	}
	defer lock.release()
	task, ok = s.Queue.Get(overlayID)
	if !ok || task == nil {
		return api.OverlayRejectOutcome{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": overlayID})
	}
	if strings.TrimSpace(task.ParentSessionID) != sessionID {
		return api.OverlayRejectOutcome{}, s.reject(OverlayPromoteSessionMismatchCode, map[string]any{"job_id": overlayID})
	}
	if task.MergeStatus != api.WorkerMergeStatusPending && task.MergeStatus != api.WorkerMergeStatusRebasing {
		return api.OverlayRejectOutcome{}, s.reject(OverlayPromoteNotPendingCode, map[string]any{
			"overlay_id": overlayID, "merge_status": string(task.MergeStatus),
		})
	}
	out := api.OverlayRejectOutcome{
		OverlayID: overlayID,
		Reason:    strings.TrimSpace(reason),
	}
	children, err := s.childOverlaysOf(ctx, sessionID, overlayID)
	if err != nil {
		return out, err
	}
	updates := []MergeStatusUpdate{{JobID: overlayID, Status: api.WorkerMergeStatusRejected}}
	for _, child := range children {
		updates = append(updates, MergeStatusUpdate{JobID: child.ID, Status: api.WorkerMergeStatusOrphaned})
		out.Orphaned = append(out.Orphaned, child.ID)
	}
	if s.Store != nil {
		commitCtx, cancel := promoteCleanupContext(ctx)
		err := s.Store.SetMergeStatuses(commitCtx, updates)
		cancel()
		if err != nil {
			return api.OverlayRejectOutcome{}, fmt.Errorf("persist rejected overlay tree: %w", err)
		}
	}
	s.clearReconcilePaths(sessionID)
	if err := s.teardownBranch(task); err != nil {
		slog.WarnContext(ctx, "rejected overlay cleanup deferred", "overlay_id", overlayID, "err", err)
	} else if s.Store != nil {
		cleanupCtx, cancel := promoteCleanupContext(ctx)
		err := s.Store.ClearWorkerWorkspace(cleanupCtx, overlayID)
		cancel()
		if err != nil {
			slog.WarnContext(ctx, "rejected overlay metadata cleanup deferred", "overlay_id", overlayID, "err", err)
		}
	}
	if s.Coord != nil {
		_ = s.Coord.ReleaseWorkerReservations(ctx, sessionID, overlayID)
		for _, childID := range out.Orphaned {
			_ = s.Coord.ReleaseWorkerReservations(ctx, sessionID, childID)
		}
	}
	sort.Strings(out.Orphaned)
	return out, nil
}

// RebaseChildren assesses stacked children against the promoted primary.
func (s *MergeService) RebaseChildren(ctx context.Context, sessionID, parentOverlayID string) ([]api.OverlayRebaseOutcome, error) {
	if s == nil || s.Queue == nil {
		return nil, fmt.Errorf("merge service not configured")
	}
	parent, ok := s.Queue.Get(parentOverlayID)
	if !ok || parent == nil {
		return nil, s.reject(OverlayPromoteNotFoundCode, map[string]any{"overlay_id": parentOverlayID})
	}
	lock, err := acquirePromoteFileLock(ctx, s.hostDataDirFor(parent))
	if err != nil {
		return nil, fmt.Errorf("acquire promote lock: %w", err)
	}
	defer lock.release()
	parent, ok = s.Queue.Get(parentOverlayID)
	if !ok || parent == nil {
		return nil, s.reject(OverlayPromoteNotFoundCode, map[string]any{"overlay_id": parentOverlayID})
	}
	if strings.TrimSpace(parent.ParentSessionID) != strings.TrimSpace(sessionID) {
		return nil, s.reject(OverlayPromoteSessionMismatchCode, map[string]any{"overlay_id": parentOverlayID})
	}
	outs, updates, err := s.assessChildRebases(ctx, sessionID, parentOverlayID)
	if err != nil || len(updates) == 0 || s.Store == nil {
		return outs, err
	}
	if err := s.Store.SetMergeStatuses(ctx, updates); err != nil {
		return nil, fmt.Errorf("persist child rebase statuses: %w", err)
	}
	return outs, nil
}

func (s *MergeService) assessChildRebases(ctx context.Context, sessionID, parentOverlayID string) ([]api.OverlayRebaseOutcome, []MergeStatusUpdate, error) {
	if s == nil || s.Queue == nil {
		return nil, nil, nil
	}
	children, err := s.childOverlaysOf(ctx, sessionID, parentOverlayID)
	if err != nil {
		return nil, nil, err
	}
	var outs []api.OverlayRebaseOutcome
	var updates []MergeStatusUpdate
	for _, child := range children {
		outcome := api.OverlayRebaseOutcome{
			OverlayID: child.ID,
		}
		assessment, aerr := s.assessChildRebase(ctx, sessionID, child)
		if aerr != nil {
			return nil, nil, aerr
		}
		if len(assessment.Conflicts) > 0 {
			outcome.Status = api.WorkerMergeStatusRebasing
			for _, c := range assessment.Conflicts {
				outcome.Conflicts = append(outcome.Conflicts, c.Path)
			}
		} else {
			outcome.Status = api.WorkerMergeStatusPending
		}
		updates = append(updates, MergeStatusUpdate{JobID: child.ID, Status: outcome.Status})
		outs = append(outs, outcome)
	}
	sort.Slice(outs, func(i, j int) bool { return outs[i].OverlayID < outs[j].OverlayID })
	sort.Slice(updates, func(i, j int) bool { return updates[i].JobID < updates[j].JobID })
	return outs, updates, nil
}

// assessChildRebase re-reads one stacked child against the moved primary,
// holding its branch for the read.
func (s *MergeService) assessChildRebase(ctx context.Context, sessionID string, child api.WorkerTask) (PromoteAssessment, error) {
	lease, err := s.holdBranch(ctx, child.ID)
	if err != nil {
		return PromoteAssessment{}, err
	}
	defer lease.Release()
	paths := s.jobPromotePaths(ctx, &child)
	return s.assessMerge(ctx, sessionID, &child, paths)
}

func enrichMergeChangedPaths(out *api.WorkerMergeResult, task api.WorkerTask, deps ChangeReportDeps, ctx context.Context) {
	if out == nil {
		return
	}
	report := BuildChangeReport(ctx, task, deps)
	if len(report.ChangedPaths) > 0 {
		out.ChangedPaths = append([]string(nil), report.ChangedPaths...)
	}
}

func (s *MergeService) childOverlaysOf(ctx context.Context, sessionID, parentOverlayID string) ([]api.WorkerTask, error) {
	if s == nil || s.Sessions == nil {
		return nil, nil
	}
	all, err := s.Sessions.ListLiveOverlaysForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var out []api.WorkerTask
	for _, t := range all {
		if t.Scope == nil {
			continue
		}
		if strings.TrimSpace(t.Scope.BaseOverlayID) == parentOverlayID {
			out = append(out, t)
		}
	}
	return out, nil
}
