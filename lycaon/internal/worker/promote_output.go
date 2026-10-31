package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/idset"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	PromoteDetailHunks = "hunks"
	PromoteDetailFull  = "full"
)

func normalizePromoteDetail(detail string) string {
	switch strings.TrimSpace(strings.ToLower(detail)) {
	case PromoteDetailFull:
		return PromoteDetailFull
	default:
		return PromoteDetailHunks
	}
}

type promoteOutputOpts struct {
	Detail string
}

func projectConflictForOutput(c PromoteConflict, detail string) api.WorkerMergeConflict {
	out := api.WorkerMergeConflict{
		Path:             c.Path,
		Reason:           c.Reason,
		Summary:          append([]string(nil), c.Summary...),
		Hunks:            append([]api.WorkerMergeHunk(nil), c.Hunks...),
		OverlapWorkerIDs: append([]string(nil), c.OverlapJobIDs...),
		BranchDelta:      BranchDelta(c.Primary, c.Branch),
		BaseDelta:        BranchDelta(c.Base, c.Branch),
		ConflictTier:     ClassifyConflictTier(c),
	}
	if normalizePromoteDetail(detail) == PromoteDetailFull {
		out.Base = truncateMergeText(c.Base)
		out.Primary = truncateMergeText(c.Primary)
		out.Branch = truncateMergeText(c.Branch)
	}
	return out
}

func buildPathStatus(assessment PromoteAssessment, applied []string) []api.WorkerPromotePathStatus {
	appliedSet := map[string]struct{}{}
	for _, p := range applied {
		p = strings.TrimSpace(p)
		if p != "" {
			appliedSet[p] = struct{}{}
		}
	}
	orders := pathOrderByPath(assessment.PathOrders)
	byPath := map[string]api.WorkerPromotePathStatus{}
	for _, p := range assessment.CleanPaths {
		if _, ok := appliedSet[p]; ok {
			byPath[p] = applyPathOrderToStatus(api.WorkerPromotePathStatus{Path: p, Status: api.WorkerPromotePathOutcomeApplied}, orders[p])
			continue
		}
		byPath[p] = applyPathOrderToStatus(api.WorkerPromotePathStatus{Path: p, Status: api.WorkerPromotePathOutcomeClean}, orders[p])
	}
	// path_status includes unchanged, applied, and conflicting paths.
	for _, c := range assessment.Conflicts {
		if _, ok := appliedSet[c.Path]; ok {
			byPath[c.Path] = applyPathOrderToStatus(api.WorkerPromotePathStatus{Path: c.Path, Status: api.WorkerPromotePathOutcomeApplied}, orders[c.Path])
			continue
		}
		byPath[c.Path] = applyPathOrderToStatus(api.WorkerPromotePathStatus{
			Path:         c.Path,
			Status:       api.WorkerPromotePathOutcomeConflict,
			HunkCount:    len(c.Hunks),
			ConflictTier: ClassifyConflictTier(c),
		}, orders[c.Path])
	}
	for p := range appliedSet {
		if _, ok := byPath[p]; !ok {
			byPath[p] = api.WorkerPromotePathStatus{Path: p, Status: api.WorkerPromotePathOutcomeApplied}
		}
	}
	out := make([]api.WorkerPromotePathStatus, 0, len(byPath))
	for _, row := range byPath {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func unionOverlapJobIDs(conflicts []PromoteConflict) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, c := range conflicts {
		for _, id := range c.OverlapJobIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func allOverlapJobIDs(assessment PromoteAssessment) []string {
	return idset.UnionSorted(unionOverlapJobIDs(assessment.Conflicts), assessment.OverlapJobIDs)
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
	eng := s.engine()
	children, err := eng.childOverlaysOf(ctx, sessionID, overlayID)
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
	eng.clearReconcilePaths(sessionID)
	if err := eng.teardownBranch(task); err != nil {
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
		_ = s.Coord.ReleaseReservations(ctx, sessionID, overlayID)
		for _, childID := range out.Orphaned {
			_ = s.Coord.ReleaseReservations(ctx, sessionID, childID)
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
	outs, updates, err := s.engine().assessChildRebases(ctx, sessionID, parentOverlayID)
	if err != nil || len(updates) == 0 || s.Store == nil {
		return outs, err
	}
	if err := s.Store.SetMergeStatuses(ctx, updates); err != nil {
		return nil, fmt.Errorf("persist child rebase statuses: %w", err)
	}
	return outs, nil
}

func (s mergeEngine) assessChildRebases(ctx context.Context, sessionID, parentOverlayID string) ([]api.OverlayRebaseOutcome, []MergeStatusUpdate, error) {
	if s.MergeService == nil || s.Queue == nil {
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
func (s mergeEngine) assessChildRebase(ctx context.Context, sessionID string, child api.WorkerTask) (PromoteAssessment, error) {
	lease, err := s.holdBranch(ctx, child.ID)
	if err != nil {
		return PromoteAssessment{}, err
	}
	defer lease.Release()
	paths := s.jobPromotePaths(ctx, &child)
	return s.assessMerge(ctx, sessionID, &child, paths)
}

func (s mergeEngine) childOverlaysOf(ctx context.Context, sessionID, parentOverlayID string) ([]api.WorkerTask, error) {
	if s.MergeService == nil || s.Sessions == nil {
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
