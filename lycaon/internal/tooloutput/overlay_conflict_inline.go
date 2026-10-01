package tooloutput

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// PromoteHighConflictHunkThreshold is the hunk count above which coordinators should
// prefer full-file promote_overlay resolutions over per-hunk patches.
const PromoteHighConflictHunkThreshold = 10

// PromoteScopedPathInlineHunkThreshold is the max hunk count for inline hunk bodies when
// preview_overlay scopes to a single path= filter.
const PromoteScopedPathInlineHunkThreshold = 30

// MaxInlineConflictSummaryLines caps digest summary lines kept in coordinator-visible JSON.
const MaxInlineConflictSummaryLines = 8

// BuildConflictDigest builds an inline-safe digest: summary line ranges only, no hunk bodies.
func BuildConflictDigest(conflicts []api.WorkerMergeConflict) []api.WorkerPromoteConflictDigest {
	if len(conflicts) == 0 {
		return nil
	}
	out := make([]api.WorkerPromoteConflictDigest, 0, len(conflicts))
	for _, c := range conflicts {
		tier := c.ConflictTier
		if tier == "" {
			tier = api.WorkerPromoteConflictTierOverlappingEdit
			if len(c.Hunks) > PromoteHighConflictHunkThreshold {
				tier = api.WorkerPromoteConflictTierLineShift
			}
		}
		out = append(out, BuildConflictDigestRow(c.Path, c.Summary, c.Hunks, c.BranchDelta, c.BaseDelta, tier))
	}
	return out
}

// BuildConflictDigestRow builds one inline digest entry without serializing hunk bodies.
func BuildConflictDigestRow(
	path string,
	summary []string,
	hunks []api.WorkerMergeHunk,
	branchDelta, baseDelta string,
	tier api.WorkerPromoteConflictTier,
) api.WorkerPromoteConflictDigest {
	path = strings.TrimSpace(path)
	summary = capSummaryLines(summary, MaxInlineConflictSummaryLines)
	if len(summary) == 0 && len(hunks) > 0 {
		summary = hunkRangeSummary(hunks, MaxInlineConflictSummaryLines)
	}
	return api.WorkerPromoteConflictDigest{
		Path:         path,
		Summary:      summary,
		BranchDelta:  strings.TrimSpace(branchDelta),
		BaseDelta:    strings.TrimSpace(baseDelta),
		ConflictTier: tier,
	}
}

func capSummaryLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return append([]string(nil), lines...)
	}
	out := append([]string(nil), lines[:max]...)
	return append(out, "…")
}

func hunkRangeSummary(hunks []api.WorkerMergeHunk, max int) []string {
	if len(hunks) == 0 {
		return nil
	}
	limit := max
	if limit <= 0 {
		limit = MaxInlineConflictSummaryLines
	}
	out := make([]string, 0, limit+1)
	for i, h := range hunks {
		if i >= limit {
			out = append(out, "…")
			break
		}
		end := h.EndLine
		if end <= 0 {
			end = h.StartLine
		}
		if end > h.StartLine {
			out = append(out, fmt.Sprintf("L%d-%d", h.StartLine, end))
			continue
		}
		out = append(out, fmt.Sprintf("L%d", h.StartLine))
	}
	return out
}
