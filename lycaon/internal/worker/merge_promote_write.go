package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session"
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
	if rows := session.NormalizePromotePathStatusRows(out.PathStatus); len(rows) > 0 {
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
