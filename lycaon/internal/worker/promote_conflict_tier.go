package worker

import (
	"strings"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxInlineBranchDeltaLines = 10

// ClassifyConflictTier labels conflict paths without language-aware parsing.
func ClassifyConflictTier(c PromoteConflict) api.WorkerPromoteConflictTier {
	if c.Reason == api.WorkerPromoteReasonArtifact {
		return api.WorkerPromoteConflictTierArtifact
	}
	hunkCount := len(c.Hunks)
	if hunkCount == 0 {
		return api.WorkerPromoteConflictTierOverlappingEdit
	}
	deltaLines := countDeltaChangeLines(BranchDelta(c.Primary, c.Branch))
	overlapHunks := countOverlappingEditHunks(c.Hunks)
	if hunkCount > tooloutput.PromoteHighConflictHunkThreshold &&
		deltaLines > 0 && deltaLines*3 < hunkCount {
		return api.WorkerPromoteConflictTierLineShift
	}
	if overlapHunks > 0 && hunkCount <= tooloutput.PromoteHighConflictHunkThreshold {
		return api.WorkerPromoteConflictTierOverlappingEdit
	}
	if hunkCount > tooloutput.PromoteHighConflictHunkThreshold {
		return api.WorkerPromoteConflictTierLineShift
	}
	return api.WorkerPromoteConflictTierOverlappingEdit
}

func countDeltaChangeLines(delta string) int {
	delta = strings.TrimSpace(delta)
	if delta == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(delta, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			n++
		}
	}
	return n
}

func countOverlappingEditHunks(hunks []api.WorkerMergeHunk) int {
	n := 0
	for _, h := range hunks {
		p := strings.TrimSpace(h.Primary)
		b := strings.TrimSpace(h.Branch)
		if p == "" || b == "" || p == b {
			continue
		}
		n++
	}
	return n
}

func digestSummaryForConflict(c PromoteConflict, tier api.WorkerPromoteConflictTier) []string {
	if tier == api.WorkerPromoteConflictTierLineShift {
		if lines := deltaLinesAsSummary(BranchDelta(c.Primary, c.Branch), tooloutput.MaxInlineConflictSummaryLines); len(lines) > 0 {
			return lines
		}
	}
	return buildConflictSummary(c)
}

func deltaLinesAsSummary(delta string, max int) []string {
	delta = strings.TrimSpace(delta)
	if delta == "" || max <= 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(delta, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(out) >= max {
			out = append(out, "…")
			break
		}
		out = append(out, line)
	}
	return out
}

func shortenDeltaForInline(delta string) string {
	lines := deltaLinesAsSummary(delta, maxInlineBranchDeltaLines)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}
