package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/pkg/api"
)

// RetainedCloseout is one closeout cycle's repair state, kept in memory across
// prompts until a report commits.
type RetainedCloseout struct {
	// Active is set once a closeout of the cycle was refused.
	Active bool
	// Attempt counts citation refusals; DocumentAttempt counts refusals of the
	// report's fields. They spend different budgets.
	Attempt         int
	DocumentAttempt int
	// PrevKey identifies the latest refusal's offenders, to notice a repeat.
	PrevKey string
	// Draft is the retained typed draft: the first narrative with the latest
	// accepted repairs.
	Draft CloseoutDraft
	// Revision counts retained source changes. RepairIDs records the revision
	// each repair ID last named an occurrence in, so an older ID is stale.
	Revision  int
	RepairIDs map[string]int
	// ForcedBy are the cycle's refusal codes, in first-seen order.
	ForcedBy []string
	// BodyRepair is set while the latest refusal asks for the body itself
	// back, so the next draft's body replaces the pinned one.
	BodyRepair bool
	// Unread are the members of the latest refused draft the report did not take.
	Unread []jsonshape.Issue
}

// Retain records a refused draft as the cycle's current revision.
func (r *RetainedCloseout) Retain(draft CloseoutDraft) {
	if r.Revision == 0 || draft.Synthesis != r.Draft.Synthesis {
		r.Revision++
	}
	r.Draft = draft
	if r.RepairIDs == nil {
		r.RepairIDs = make(map[string]int, len(draft.Occurrences))
	}
	for _, occurrence := range draft.Occurrences {
		if occurrence.RepairID != "" {
			r.RepairIDs[occurrence.RepairID] = r.Revision
		}
	}
}

// PresentationObservation names each presentation code's reject observation. A
// presentation refusal asks for the body back, not a citation.
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

// AssembleRetainedCloseout preserves the answer and its resolvable references
// after citation repair is exhausted. Unresolved protocol nodes leave the display;
// an unobserved link keeps its label. The rejected raw draft stays in the
// existing inspection rows.
func AssembleRetainedCloseout(roots evidence.CitationRoots, surface string, ev CloseoutEvidence, draft CloseoutDraft, presentable []string, code string, retries int) (CoordinatorCompletionReport, *api.CitationGrounding) {
	resolved := ResolveDraftCitations(roots, surface, draft, ev, presentable)
	originalEval := EvaluateDraftCitations(draft, resolved)
	report := draft.Report()
	report.Synthesis = strings.TrimSpace(PresentCloseoutMarkdown(draft, resolved))
	report.CitedEvidence, report.CitedURLs, report.ArtifactIDs = AcceptedDraftReferences(draft, resolved)
	grounding := BuildCloseoutCitationGrounding(roots, surface, report, ev, originalEval)
	if TypedCitationChannelsEmpty(len(report.CitedEvidence), len(report.CitedURLs)) {
		report = BindObservedSample(report, ev.Ledger, roots)
		attached := BuildObservedAutobindGrounding(roots, ev.Ledger, report)
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
	return report, grounding
}
