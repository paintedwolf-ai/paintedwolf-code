package guidance

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/pkg/api"
)

// RetainedCloseout is one closeout cycle's repair state, kept across prompts
// until a report commits.
type RetainedCloseout struct {
	// Active is set once a closeout of the cycle was refused.
	Active bool
	// Attempt counts citation refusals; DocumentAttempt counts refusals of the
	// report's fields. They spend different budgets.
	Attempt         int
	DocumentAttempt int
	// PrevKey identifies the latest refusal's offenders, to notice a repeat.
	PrevKey string
	// Drafted is the retained report envelope: the first body with the latest
	// citations, and the latest document fields once a document was refused.
	Drafted string
	// ForcedBy are the cycle's refusal codes, in first-seen order.
	ForcedBy []string
	// Unread are the members of the latest refused draft the report did not take.
	Unread []jsonshape.Issue
}

// RetainCloseoutDraft keeps the first report with the latest proposed
// citations, and the latest document fields once a document was rejected.
func RetainCloseoutDraft(previous, next string, documentRepair bool) string {
	if previous == "" {
		return next
	}
	updated, ok := ParseCoordinatorCompletionReport(next)
	if !ok {
		return previous
	}
	if original, ok := ParseCoordinatorCompletionReport(previous); ok {
		updated = PinCloseoutReport(original, updated, documentRepair)
	} else {
		updated.Synthesis = UsableCloseoutSynthesis(previous)
	}
	raw, err := MarshalCoordinatorCompletionReport(updated)
	if err != nil {
		return previous
	}
	return raw
}

// PinCloseoutReport keeps the first report through citation repair, so a
// retry that only fixes references cannot rewrite the conclusions. When a
// document field was rejected, the retry's document fields are the repair.
func PinCloseoutReport(original, repaired CoordinatorCompletionReport, documentRepair bool) CoordinatorCompletionReport {
	original.CitedEvidence = repaired.CitedEvidence
	original.CitedURLs = repaired.CitedURLs
	if documentRepair {
		original.Headline = repaired.Headline
		original.Summary = repaired.Summary
		original.Findings = repaired.Findings
		original.Limits = repaired.Limits
		original.Ask = repaired.Ask
		original.SetAsides = repaired.SetAsides
	}
	return original
}

// AssembleRetainedCloseout preserves the report and its resolvable references
// after citation repair is exhausted.
func AssembleRetainedCloseout(roots evidence.CitationRoots, surface string, ev CloseoutEvidence, draft CoordinatorCompletionReport, code string, retries int) (CoordinatorCompletionReport, *api.CitationGrounding) {
	originalEval := EvaluateCloseoutCitations(roots, surface, draft, ev)
	retained := make([]CoordinatorCitedEvidence, 0, len(draft.CitedEvidence))
	for _, citation := range draft.CitedEvidence {
		probe := CoordinatorCompletionReport{Synthesis: draft.Synthesis, CitedEvidence: []CoordinatorCitedEvidence{citation}}
		eval := EvaluateCloseoutCitations(roots, surface, probe, ev)
		if eval.Code == "" && !eval.CitationUnverifiable {
			retained = append(retained, citation)
		}
	}
	draft.CitedEvidence = retained
	urls := make([]string, 0, len(draft.CitedURLs))
	for _, url := range draft.CitedURLs {
		if ev.URLSeen(url) {
			urls = append(urls, url)
		}
	}
	draft.CitedURLs = urls
	grounding := BuildCloseoutCitationGrounding(roots, surface, draft, ev, originalEval)
	if TypedCitationChannelsEmpty(len(draft.CitedEvidence), len(draft.CitedURLs)) {
		draft = BindObservedSample(draft, ev.Ledger, roots)
		attached := BuildObservedAutobindGrounding(roots, ev.Ledger, draft)
		if grounding != nil {
			attached.Checks = grounding.Checks
			attached.Verification = grounding.Verification
		}
		grounding = attached
	}
	if grounding == nil {
		grounding = &api.CitationGrounding{}
	}
	grounding.Traced = false
	grounding.HostAssembled = true
	grounding.HintCode = code
	grounding.RetryCount = retries
	return draft, grounding
}
