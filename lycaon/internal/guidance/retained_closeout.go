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
	// Drafted is the retained report envelope: the pinned body with the latest
	// citations, and the latest document fields once a document was refused.
	Drafted string
	// ForcedBy are the cycle's refusal codes, in first-seen order.
	ForcedBy []string
	// BodyRepair is set while the latest refusal asks for the body itself
	// back, so the next draft's body replaces the pinned one.
	BodyRepair bool
	// Unread are the members of the latest refused draft the report did not take.
	Unread []jsonshape.Issue
}

// Repair says which parts of the retained report the next draft replaces.
// Citations are always the draft's own.
func (r RetainedCloseout) Repair() CloseoutRepair {
	return CloseoutRepair{Document: RepairsReportDocument(r.ForcedBy), Body: r.BodyRepair}
}

// CloseoutRepair names the parts of a retained report a draft may replace.
type CloseoutRepair struct {
	// Document: the report's fields, once a document field was refused on the cycle.
	Document bool
	// Body: the answer itself, when the latest refusal was of how the body presents images.
	Body bool
}

// RetainCloseoutDraft keeps the pinned report with the latest proposed
// citations, replacing only the parts repair names.
func RetainCloseoutDraft(previous, next string, repair CloseoutRepair) string {
	if previous == "" {
		return next
	}
	updated, ok := ParseCoordinatorCompletionReport(next)
	if !ok {
		return previous
	}
	if original, ok := ParseCoordinatorCompletionReport(previous); ok {
		updated = PinCloseoutReport(original, updated, repair)
	} else if !repair.Body {
		updated.Synthesis = UsableCloseoutSynthesis(previous)
	}
	raw, err := MarshalCoordinatorCompletionReport(updated)
	if err != nil {
		return previous
	}
	return raw
}

// PinCloseoutReport keeps the pinned report through repair, so a retry that
// only fixes references cannot rewrite the conclusions. A document repair
// takes the retry's document fields; a body repair takes its body and the
// artifacts it presents.
func PinCloseoutReport(original, repaired CoordinatorCompletionReport, repair CloseoutRepair) CoordinatorCompletionReport {
	original.CitedEvidence = repaired.CitedEvidence
	original.CitedURLs = repaired.CitedURLs
	if repair.Document {
		original.Headline = repaired.Headline
		original.Summary = repaired.Summary
		original.Findings = repaired.Findings
		original.Limits = repaired.Limits
		original.Ask = repaired.Ask
		original.SetAsides = repaired.SetAsides
	}
	if repair.Body {
		original.Synthesis = repaired.Synthesis
		original.ArtifactIDs = repaired.ArtifactIDs
	}
	return original
}

// PresentationObservation names each presentation code's reject observation,
// the fact its closeout policy matches. A presentation refusal asks for the
// body back, not a citations fence.
func PresentationObservation(code string) string {
	switch code {
	case PresentMarkdownEmbedCode:
		return "present_markdown_embed"
	case PresentImageUnobservedCode:
		return "present_image_unobserved"
	default:
		return ""
	}
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
