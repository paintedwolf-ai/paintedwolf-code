package worker

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func buildReadyResolutions(assessment PromoteAssessment) []api.WorkerReadyResolution {
	var out []api.WorkerReadyResolution
	for _, rel := range assessment.CleanPaths {
		out = append(out, api.WorkerReadyResolution{
			Path:      rel,
			Status:    api.WorkerPromotePathOutcomeClean,
			PathClass: "text",
			Action:    api.WorkerPromoteResolutionActionKeepBoth,
			Advisory:  "Compatible path — lands when the whole promotion can commit.",
		})
	}
	for _, c := range assessment.Conflicts {
		out = append(out, readyResolutionForConflict(c))
	}
	return out
}

func readyResolutionForConflict(c PromoteConflict) api.WorkerReadyResolution {
	tier := ClassifyConflictTier(c)
	if tier == api.WorkerPromoteConflictTierArtifact {
		return api.WorkerReadyResolution{
			Path:         c.Path,
			Status:       api.WorkerPromotePathOutcomeConflict,
			PathClass:    "artifact",
			ConflictTier: tier,
			Action:       api.WorkerPromoteResolutionActionDrop,
			NeedsReview:  true,
			Advisory:     "Binary/opaque path — keep_ours or drop skips the worker's bytes.",
		}
	}
	proposed, reconciled := ProposeReconciledContent(c)
	ready := api.WorkerReadyResolution{
		Path:         c.Path,
		Status:       api.WorkerPromotePathOutcomeConflict,
		PathClass:    "text",
		ConflictTier: tier,
	}
	if reconciled && strings.TrimSpace(proposed) != "" {
		// Reconciled content preserves both sides.
		ready.Action = api.WorkerPromoteResolutionActionKeepBoth
		ready.ProposedContent = proposed
		ready.ProposedReconciled = true
		ready.NeedsReview = false
		ready.Advisory = "Both sides merge cleanly — keep_both lands the union (auto-applied by default)."
	} else {
		// Unreconciled content requires an explicit choice.
		ready.NeedsReview = true
		ready.Advisory = "Overlapping edits — supply merged content or hunks with keep_both, or choose keep_theirs, keep_ours, or drop."
	}
	return ready
}

func readyResolutionsToPromote(auto []api.WorkerReadyResolution, autoOnly bool) []api.WorkerPromoteResolution {
	var out []api.WorkerPromoteResolution
	for _, r := range auto {
		if r.Status == api.WorkerPromotePathOutcomeClean {
			continue
		}
		if autoOnly && r.NeedsReview {
			continue
		}
		if r.Action == "" {
			continue
		}
		res := api.WorkerPromoteResolution{Path: r.Path, Action: r.Action}
		switch r.Action {
		case api.WorkerPromoteResolutionActionKeepBoth:
			if len(r.SuggestedHunks) > 0 {
				res.Hunks = r.SuggestedHunks
				res.Action = ""
			} else if strings.TrimSpace(r.ProposedContent) != "" {
				res.Content = r.ProposedContent
			}
		case api.WorkerPromoteResolutionActionDrop, api.WorkerPromoteResolutionActionKeepOurs, api.WorkerPromoteResolutionActionKeepTheirs:
		}
		if res.Action == "" && strings.TrimSpace(res.Content) == "" && len(res.Hunks) == 0 {
			continue
		}
		out = append(out, res)
	}
	return out
}

func unionPromoteResolutions(a, b []api.WorkerPromoteResolution) []api.WorkerPromoteResolution {
	byPath := map[string]api.WorkerPromoteResolution{}
	for _, r := range a {
		if p := strings.TrimSpace(r.Path); p != "" {
			byPath[p] = r
		}
	}
	for _, r := range b {
		if p := strings.TrimSpace(r.Path); p != "" {
			byPath[p] = r
		}
	}
	out := make([]api.WorkerPromoteResolution, 0, len(byPath))
	for _, p := range sortedPromotePathKeys(byPath) {
		out = append(out, byPath[p])
	}
	return out
}

func sortedPromotePathKeys(m map[string]api.WorkerPromoteResolution) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ProposeReconciledContent returns reconciled content for an overlay conflict.
func ProposeReconciledContent(c PromoteConflict) (proposed string, reconciled bool) {
	merged, _, clean := ThreeWayMerge(c.Base, c.Primary, c.Branch)
	if clean {
		return merged, true
	}
	return "", false
}
