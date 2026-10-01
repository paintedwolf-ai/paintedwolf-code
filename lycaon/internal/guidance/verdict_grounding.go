package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// Structured reject codes for terminal review_loop verdict grounding.
const (
	VerdictCitationsRequiredCode  = "SUBMIT_VERDICT_CITATIONS_REQUIRED"
	VerdictCitationUngroundedCode = "SUBMIT_VERDICT_CITATION_UNGROUNDED"
	VerdictReviewerUncitedCode    = "SUBMIT_VERDICT_REVIEWER_UNCITED"
)

// verdictSampleCap bounds offender and observed-handle samples in rejects.
const verdictSampleCap = 6

// ReviewerEvidence is one owed reviewer with the evidence its succeeded legs
// produced: the namespaces their ledgers were merged under, and the raw leg
// ledgers for path/URL membership.
type ReviewerEvidence struct {
	Agent   string
	LegIDs  []string
	Ledgers []evidence.Ledger
}

// VerdictGroundingEval is the host audit of a terminal verdict's citations.
// Code is empty when the verdict grounds; there is no host-authored fallback —
// an adjudication the host cannot trace is rejected, never rewritten.
type VerdictGroundingEval struct {
	Grounding        *api.CitationGrounding
	Code             string
	UngroundedCount  int
	UngroundedSample []string
	UncitedReviewers []string
	ObservedHandles  []string
}

// EvaluateVerdictGrounding validates a terminal verdict's citations against the
// sojourn evidence union and requires every owed reviewer's evidence to be
// cited at least once — through a path or URL its legs observed, or a
// leg-qualified handle.
func EvaluateVerdictGrounding(
	roots evidence.CitationRoots,
	ev CloseoutEvidence,
	cited []api.CitationGroundingCitedEvidence,
	citedURLs []string,
	reviewers []ReviewerEvidence,
) VerdictGroundingEval {
	out := VerdictGroundingEval{ObservedHandles: observedHandleSample(ev.Ledger)}
	citedURLs = trimNonEmpty(citedURLs)
	if len(cited) == 0 && len(citedURLs) == 0 {
		out.Code = VerdictCitationsRequiredCode
		out.UncitedReviewers = reviewerAgents(reviewers)
		return out
	}

	var wire []api.CitationGroundingCitedEvidence
	var grounded []evidence.Resolution
	for _, item := range cited {
		finding := WorkerFindingInput{
			Evidence: strings.TrimSpace(item.Handle),
			Path:     strings.TrimSpace(item.Path),
			Line:     item.Line,
			Excerpt:  strings.TrimSpace(item.Excerpt),
		}
		res := resolveWorkerFindingCitation(roots, finding, ev.Ledger)
		if !res.Verdict.Grounded() {
			out.UngroundedCount++
			if len(out.UngroundedSample) < verdictSampleCap {
				out.UngroundedSample = append(out.UngroundedSample, FormatResolutionOffender(res, finding))
			}
			continue
		}
		grounded = append(grounded, res)
		wire = append(wire, api.CitationGroundingCitedEvidence{
			Handle:   res.Handle,
			Path:     res.Path,
			Line:     res.Line,
			Excerpt:  res.Excerpt,
			Verdict:  WireCitationVerdict(res.Verdict),
			Openable: evidence.IsOpenablePath(roots, res.Path),
		})
	}
	var groundedURLs []string
	for _, url := range citedURLs {
		if !ev.URLSeen(url) {
			out.UngroundedCount++
			if len(out.UngroundedSample) < verdictSampleCap {
				out.UngroundedSample = append(out.UngroundedSample, url)
			}
			continue
		}
		groundedURLs = append(groundedURLs, url)
	}
	if out.UngroundedCount > 0 {
		out.Code = VerdictCitationUngroundedCode
		return out
	}

	out.UncitedReviewers = uncitedReviewers(roots, reviewers, grounded, groundedURLs)
	if len(out.UncitedReviewers) > 0 {
		out.Code = VerdictReviewerUncitedCode
		return out
	}

	checks := []api.CitationGroundingCheck{
		{
			ID:     "verdict_citations",
			Label:  "Verdict citations",
			Status: api.CitationGroundingCheckStatusPassed,
		},
		{
			ID:      "reviewer_coverage",
			Label:   "Reviewer evidence cited",
			Status:  api.CitationGroundingCheckStatusPassed,
			Matched: reviewerAgents(reviewers),
			Vacuous: len(reviewers) == 0,
		},
	}
	out.Grounding = &api.CitationGrounding{
		Traced:        true,
		Checks:        stampCitationKind(checks),
		CitedEvidence: wire,
		CitedURLs:     groundedURLs,
	}
	return out
}

// uncitedReviewers lists owed reviewers none of whose leg evidence is cited: no
// grounded citation resolves into their leg namespaces or observed paths, and
// no cited URL was seen by their legs.
func uncitedReviewers(
	roots evidence.CitationRoots,
	reviewers []ReviewerEvidence,
	grounded []evidence.Resolution,
	groundedURLs []string,
) []string {
	var out []string
	for _, r := range reviewers {
		if strings.TrimSpace(r.Agent) == "" {
			continue
		}
		if !reviewerCited(roots, r, grounded, groundedURLs) {
			out = append(out, r.Agent)
		}
	}
	return out
}

func reviewerCited(
	roots evidence.CitationRoots,
	r ReviewerEvidence,
	grounded []evidence.Resolution,
	groundedURLs []string,
) bool {
	for _, res := range grounded {
		handle := strings.TrimSpace(res.Handle)
		for _, leg := range r.LegIDs {
			if leg = strings.TrimSpace(leg); leg != "" && strings.HasPrefix(handle, leg+":") {
				return true
			}
		}
		path := strings.TrimSpace(res.Path)
		if path == "" {
			continue
		}
		for _, ledger := range r.Ledgers {
			if evidence.LedgerPathKey(roots, path, ledger.ByPath) != "" {
				return true
			}
		}
	}
	for _, url := range groundedURLs {
		for _, ledger := range r.Ledgers {
			if ledger.URLSeen(url) {
				return true
			}
		}
	}
	return false
}

func reviewerAgents(reviewers []ReviewerEvidence) []string {
	var out []string
	for _, r := range reviewers {
		if agent := strings.TrimSpace(r.Agent); agent != "" {
			out = append(out, agent)
		}
	}
	return out
}

func observedHandleSample(ev evidence.Ledger) []string {
	handles := evidence.HandlesSorted(ev)
	if len(handles) > verdictSampleCap {
		handles = handles[:verdictSampleCap]
	}
	return append([]string(nil), handles...)
}

func trimNonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
