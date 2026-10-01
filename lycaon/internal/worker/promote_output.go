package worker

import (
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
