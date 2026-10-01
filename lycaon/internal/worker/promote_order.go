package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/idset"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromotePathOrder is per-path merge ordering relative to pending sibling overlays.
type PromotePathOrder struct {
	Path             string
	Order            api.WorkerPromoteOrderKind
	PromoteAfter     []string
	BlockedBy        []string
	PromoteOrderNote string
}

func (s *MergeService) pendingOverlayByID(ctx context.Context, sessionID, jobID string) *api.WorkerTask {
	if s == nil || s.Sessions == nil {
		return nil
	}
	tasks, err := s.Sessions.ListPendingOverlayPromote(ctx, sessionID)
	if err != nil {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	for _, task := range tasks {
		if task.ID == jobID {
			cp := task
			return &cp
		}
	}
	return nil
}

func siblingVerdictForPath(ctx context.Context, svc *MergeService, task *api.WorkerTask, rel string) (promotePathVerdict, bool) {
	if task == nil {
		return promotePathVerdict{}, false
	}
	baseline, err := workspacebaseline.Open(ctx, task.WorkspaceBaselinePath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath))
	if err != nil {
		return promotePathVerdict{}, false
	}
	defer func() { _ = baseline.Close() }()
	roots := []projectroot.RootRef(nil)
	if svc != nil {
		roots = svc.taskRootRefs(ctx, task)
	}
	promote := PromoteRootsForTask(task, roots)
	verdict := assessPromotePath3Way(ctx, promote, task, rel, baseline)
	return verdict, true
}

func computePathPromoteOrder(
	ctx context.Context,
	svc *MergeService,
	sessionID string,
	task *api.WorkerTask,
	rel string,
	baseline *workspacebaseline.Reader,
	verdict promotePathVerdict,
	siblingIDs []string,
) PromotePathOrder {
	out := PromotePathOrder{Path: rel, Order: api.WorkerPromoteOrderIndependent}
	siblingIDs = idset.UnionSorted(nil, siblingIDs)
	if len(siblingIDs) == 0 {
		return out
	}
	if svc == nil {
		return tierAPathOrder(rel, siblingIDs)
	}

	promote := PromoteRootsForTask(task, svc.taskRootRefs(ctx, task))
	_, branchRaw, branchPresent, err := promote.ReadPairBytes(task, rel)
	if err != nil || !branchPresent {
		return tierAPathOrder(rel, siblingIDs)
	}
	branchText, ok := openPromoteText(branchRaw)
	if !ok {
		return tierAPathOrder(rel, siblingIDs)
	}
	branch := branchText.content

	if verdict.isConflict {
		return simulateCleanAfter(ctx, svc, sessionID, task, rel, baseline, branch, siblingIDs)
	}
	if verdict.noop {
		return out
	}

	var blockedBy []string
	inconclusive := false
	for _, sibID := range siblingIDs {
		sibTask := svc.pendingOverlayByID(ctx, sessionID, sibID)
		sibVerdict, ok := siblingVerdictForPath(ctx, svc, sibTask, rel)
		if !ok {
			inconclusive = true
			continue
		}
		if sibVerdict.noop {
			continue
		}
		if sibVerdict.isConflict {
			inconclusive = true
			continue
		}
		after := assessPromotePathWithContent(ctx, sibVerdict.merged, branch, rel, baseline, true, true)
		if after.isConflict {
			blockedBy = append(blockedBy, sibID)
		}
	}
	blockedBy = idset.UnionSorted(nil, blockedBy)
	if len(blockedBy) > 0 {
		out.Order = api.WorkerPromoteOrderCleanIfFirst
		out.BlockedBy = blockedBy
		out.PromoteOrderNote = fmt.Sprintf("clean vs current primary; promote before %s or re-preview after they land", strings.Join(blockedBy, ", "))
		return out
	}
	if inconclusive {
		return tierAPathOrder(rel, siblingIDs)
	}
	out.PromoteOrderNote = "clean vs current primary; sibling order does not change outcome"
	return out
}

func tierAPathOrder(rel string, siblingIDs []string) PromotePathOrder {
	return PromotePathOrder{
		Path:             rel,
		Order:            api.WorkerPromoteOrderSequential,
		PromoteAfter:     append([]string(nil), siblingIDs...),
		PromoteOrderNote: fmt.Sprintf("shares `%s` with pending siblings — promote sequentially and re-preview after each landing", rel),
	}
}

func simulateCleanAfter(
	ctx context.Context,
	svc *MergeService,
	sessionID string,
	task *api.WorkerTask,
	rel string,
	baseline *workspacebaseline.Reader,
	branch string,
	siblingIDs []string,
) PromotePathOrder {
	out := PromotePathOrder{Path: rel, Order: api.WorkerPromoteOrderIndependent}
	primaryRaw, _, branchPresent, err := readPromotePairBytes(task.WorkspacePath, task.WorkspaceRoot, rel)
	if err != nil || !branchPresent {
		return out
	}
	primaryText, ok := openPromoteText(primaryRaw)
	if !ok {
		return out
	}
	simulated := primaryText.content
	var promoteAfter []string
	for _, sibID := range siblingIDs {
		sibTask := svc.pendingOverlayByID(ctx, sessionID, sibID)
		sibVerdict, ok := siblingVerdictForPath(ctx, svc, sibTask, rel)
		if !ok || sibVerdict.noop || sibVerdict.isConflict {
			continue
		}
		simulated = sibVerdict.merged
		promoteAfter = append(promoteAfter, sibID)
	}
	if len(promoteAfter) == 0 {
		return out
	}
	after := assessPromotePathWithContent(ctx, simulated, branch, rel, baseline, true, true)
	if after.isConflict || after.noop {
		return out
	}
	out.Order = api.WorkerPromoteOrderCleanAfter
	out.PromoteAfter = idset.UnionSorted(nil, promoteAfter)
	out.PromoteOrderNote = fmt.Sprintf("conflict vs current primary; clean after %s promote", strings.Join(out.PromoteAfter, ", "))
	return out
}

func enrichAssessmentPromoteOrders(
	ctx context.Context,
	svc *MergeService,
	sessionID string,
	task *api.WorkerTask,
	assessment *PromoteAssessment,
) {
	if assessment == nil || task == nil {
		return
	}
	baseline, err := workspacebaseline.Open(ctx, task.WorkspaceBaselinePath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath))
	if err != nil {
		return
	}
	defer func() { _ = baseline.Close() }()
	assessment.PathOrders = nil

	addOrder := func(rel string, verdict promotePathVerdict, siblings []string) {
		order := computePathPromoteOrder(ctx, svc, sessionID, task, rel, baseline, verdict, siblings)
		if order.Order == api.WorkerPromoteOrderIndependent && order.PromoteOrderNote == "" && len(siblings) == 0 {
			return
		}
		assessment.PathOrders = append(assessment.PathOrders, order)
	}

	promote := PromoteRootsForTask(task, svc.taskRootRefs(ctx, task))
	for _, rel := range assessment.CleanPaths {
		verdict := assessPromotePath3Way(ctx, promote, task, rel, baseline)
		var siblings []string
		if svc != nil {
			siblings = svc.overlapJobsForPath(ctx, sessionID, rel, task.ID)
		}
		addOrder(rel, verdict, siblings)
	}
	for _, c := range assessment.Conflicts {
		verdict := promotePathVerdict{isConflict: true, conflict: c}
		siblings := c.OverlapJobIDs
		if svc != nil && len(siblings) == 0 {
			siblings = svc.overlapJobsForPath(ctx, sessionID, c.Path, task.ID)
		}
		addOrder(c.Path, verdict, siblings)
	}
}

func pathOrderByPath(orders []PromotePathOrder) map[string]PromotePathOrder {
	out := map[string]PromotePathOrder{}
	for _, row := range orders {
		if row.Path == "" {
			continue
		}
		out[row.Path] = row
	}
	return out
}

func aggregateOverlayPromoteOrder(orders []PromotePathOrder) (api.WorkerPromoteOrderKind, []string, []string, string) {
	if len(orders) == 0 {
		return api.WorkerPromoteOrderIndependent, nil, nil, ""
	}
	rank := map[api.WorkerPromoteOrderKind]int{
		api.WorkerPromoteOrderIndependent:  0,
		api.WorkerPromoteOrderSequential:   1,
		api.WorkerPromoteOrderCleanAfter:   2,
		api.WorkerPromoteOrderCleanIfFirst: 3,
	}
	best := api.WorkerPromoteOrderIndependent
	var promoteAfter, blockedBy []string
	var notes []string
	for _, row := range orders {
		if rank[row.Order] > rank[best] {
			best = row.Order
		}
		promoteAfter = idset.UnionSorted(promoteAfter, row.PromoteAfter)
		blockedBy = idset.UnionSorted(blockedBy, row.BlockedBy)
		if note := strings.TrimSpace(row.PromoteOrderNote); note != "" {
			notes = append(notes, note)
		}
	}
	sort.Strings(notes)
	note := strings.Join(notes, "; ")
	if len(notes) > 2 {
		note = notes[0] + "; …"
	}
	return best, promoteAfter, blockedBy, note
}

func applyPathOrderToStatus(row api.WorkerPromotePathStatus, order PromotePathOrder) api.WorkerPromotePathStatus {
	if order.Order == "" || order.Order == api.WorkerPromoteOrderIndependent {
		if order.PromoteOrderNote != "" {
			row.PromoteOrder = order.Order
			row.PromoteOrderNote = order.PromoteOrderNote
		}
		return row
	}
	row.PromoteOrder = order.Order
	row.PromoteAfter = append([]string(nil), order.PromoteAfter...)
	row.BlockedBy = append([]string(nil), order.BlockedBy...)
	row.PromoteOrderNote = order.PromoteOrderNote
	return row
}
