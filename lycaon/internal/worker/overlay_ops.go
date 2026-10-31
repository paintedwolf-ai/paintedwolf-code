package worker

import (
	"context"
	"errors"
	"fmt"
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
	eng := s.engine()
	work, err := eng.resolvePromoteWork(ctx, sessionID, overlayID)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	defer work.lease.Release()

	plans, applied, conflictOut, err := eng.preparePromoteOverlayPlans(ctx, work, in)
	if err != nil {
		return conflictOut, err
	}
	targets, err := s.snapshotPromoteTargets(work.task, work.roots, work.paths)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	work.targets = targets
	if len(applied) == 0 {
		changed, err := session.InspectOverlayChanges(ctx, work.task, work.roots)
		if err != nil {
			return api.WorkerMergeResult{}, err
		}
		if len(changed) > 0 {
			return api.WorkerMergeResult{}, s.reject(OverlayPromoteNoPathsCode, map[string]any{"job_id": overlayID, "remaining_overlay_paths": changed})
		}
		return eng.finalizeEmptyPromote(ctx, sessionID, work.task, overlayID, in.Detail)
	}
	return eng.applyPromoteOverlayPlans(ctx, work, in, plans, applied)
}

func (s mergeEngine) preparePromoteOverlayPlans(
	ctx context.Context,
	work promoteWork,
	in api.PromoteOverlayInput,
) ([]promoteMutation, []string, api.WorkerMergeResult, error) {
	allowed, err := allowedPromotePathSet(work.task, work.roots, work.paths)
	if err != nil {
		return nil, nil, api.WorkerMergeResult{}, err
	}
	callerResolutions := expandResolutionDropFolders(in.Resolutions, allowed)
	assessment, err := s.assessPromoteOverlay(ctx, work, callerResolutions)
	if err != nil {
		return nil, nil, api.WorkerMergeResult{}, err
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
		out := api.WorkerMergeResult{JobID: work.overlayID, Mode: "merge", AgentType: work.task.AgentType, Paths: work.paths}
		fillMergeResult(&out, assessment, promoteOutputOpts{Detail: in.Detail})
		s.attachPromoteSpill(s.hostDataDirFor(work.task), work.overlayID, &out, assessment)
		s.registerReconcilePaths(work.sessionID, assessment)
		s.recordPromoteOutput(work.sessionID, work.overlayID, &out)
		return nil, nil, out, s.rejectConflict(work.overlayID, assessment)
	}
	return s.buildValidatedPromotePlans(ctx, work, assessment, resolutions, resolutionByPath)
}

func (s mergeEngine) assessPromoteOverlay(
	ctx context.Context,
	work promoteWork,
	callerResolutions []api.WorkerPromoteResolution,
) (PromoteAssessment, error) {
	callerPaths := map[string]struct{}{}
	for _, r := range callerResolutions {
		if p := strings.TrimSpace(r.Path); p != "" {
			callerPaths[p] = struct{}{}
		}
	}
	var mergePaths []string
	for _, p := range work.paths {
		if _, ok := callerPaths[p]; !ok {
			mergePaths = append(mergePaths, p)
		}
	}
	if len(mergePaths) == 0 {
		return PromoteAssessment{}, nil
	}
	return s.assessMerge(ctx, work.sessionID, work.task, mergePaths)
}

func (s mergeEngine) buildValidatedPromotePlans(
	ctx context.Context,
	work promoteWork,
	assessment PromoteAssessment,
	resolutions []api.WorkerPromoteResolution,
	resolutionByPath map[string]api.WorkerPromoteResolution,
) ([]promoteMutation, []string, api.WorkerMergeResult, error) {
	validated, err := validatePromoteResolutionPaths(work.task, work.roots, work.paths, resolutions)
	if err != nil {
		return nil, nil, api.WorkerMergeResult{}, err
	}
	resolutionPlans, err := preparePromoteResolutions(work.task, work.roots, validated, resolutionByPath)
	if err != nil {
		return nil, nil, api.WorkerMergeResult{}, s.reject(OverlayPromoteResolutionsRequiredCode, map[string]any{
			"job_id": work.overlayID,
			"reason": err.Error(),
		})
	}
	mergePlans, err := prepareMergeMutations(work.task, assessment)
	if err != nil {
		return nil, nil, api.WorkerMergeResult{}, err
	}
	plans := append(append([]promoteMutation(nil), mergePlans...), resolutionPlans...)
	if issue := validatePromoteSyntax(ctx, plans); issue != nil {
		return nil, nil, api.WorkerMergeResult{}, s.reject(OverlayPromoteSyntaxUnhealthyCode, issue.rejectData(work.overlayID))
	}
	return plans, unionPromotePaths(assessment.CleanPaths, validated), api.WorkerMergeResult{}, nil
}

func (s mergeEngine) applyPromoteOverlayPlans(ctx context.Context, work promoteWork, in api.PromoteOverlayInput, plans []promoteMutation, applied []string) (api.WorkerMergeResult, error) {
	// The hold outlives any rollback of landed bytes, so observation never sees them first.
	documents, err := s.holdPromotedDocuments(ctx, work.task, work.targets, applied)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	defer documents.release(ctx, false)
	claim, claimCtx, err := s.beginMergeClaim(ctx, work.overlayID)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	revertPending := func() error { return s.releaseMergeClaim(ctx, claim) }
	if s.Captures != nil {
		s.Captures.RecordPromotedPrimaryPaths(ctx, work.sessionID, promoteMutationPaths(plans))
	}
	promote := PromoteRootsForTask(work.task, work.roots)
	tx, err := beginPromoteTransaction(s.hostDataDirFor(work.task), work.overlayID, plans)
	if err != nil {
		return api.WorkerMergeResult{}, errors.Join(err, revertPending())
	}
	if err := tx.apply(claimCtx, promote, work.task); err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, work.task, revertPending, err)
	}
	if len(plans) > 0 {
		notifyMergeWorktreeChanged(ctx, work.task, work.roots)
	}
	facts, err := s.promotedSourceFacts(claimCtx, work.sessionID, in.ToolCallID, sourceworkspace.ID(work.task.ProjectID, work.roots), in.UserTurn, work.task, work.targets, applied, documents)
	if err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, work.task, revertPending, err)
	}
	rebased, childStatuses, err := s.assessChildRebases(claimCtx, work.sessionID, work.overlayID)
	if err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, work.task, revertPending, err)
	}
	out := api.WorkerMergeResult{JobID: work.overlayID, Mode: "merge", AgentType: work.task.AgentType, Paths: work.paths, Rebased: rebased}
	finishIn := promoteFinishInput{sessionID: work.sessionID, task: work.task, jobID: work.overlayID, out: &out, resolved: applied, landed: promoteMutationPaths(plans), detail: in.Detail, claim: claim, facts: facts, childStatuses: childStatuses}
	if err := s.finishPromoteState(claimCtx, finishIn); err != nil {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, work.task, revertPending, err)
	}
	if out.Status != api.WorkerMergeStatusMerged {
		return api.WorkerMergeResult{}, s.failPromoteTransaction(tx, promote, work.task, revertPending, fmt.Errorf("promote plan incomplete after apply"))
	}
	s.closeCommittedPromoteTransaction(ctx, tx, work.overlayID)
	if len(facts.files) > 0 {
		out.OverlayPromotion = &api.OverlayPromotion{Files: facts.files}
	}
	s.recordPromoteOutput(work.sessionID, work.overlayID, &out)
	return out, nil
}

type promoteWork struct {
	sessionID string
	overlayID string
	task      *api.WorkerTask
	roots     []projectroot.RootRef
	paths     []string
	targets   map[string]promoteTarget
	// lease keeps the branch tree on disk until the promote is over.
	lease *BranchLease
}

func (s mergeEngine) resolvePromoteWork(ctx context.Context, sessionID, overlayID string) (promoteWork, error) {
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
		sessionID: sessionID, overlayID: overlayID,
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
func (s mergeEngine) finalizeEmptyPromote(ctx context.Context, sessionID string, task *api.WorkerTask, jobID, detail string) (api.WorkerMergeResult, error) {
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
	if err := s.finishPromoteState(claimCtx, promoteFinishInput{
		sessionID: sessionID, task: task, jobID: jobID, out: &out,
		detail: detail, claim: claim, childStatuses: childStatuses,
	}); err != nil {
		return out, errors.Join(err, s.releaseMergeClaim(ctx, claim))
	}
	s.recordPromoteOutput(sessionID, jobID, &out)
	return out, nil
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
