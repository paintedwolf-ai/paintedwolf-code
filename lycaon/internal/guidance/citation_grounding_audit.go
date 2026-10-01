package guidance

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// TypedCitationChannelsEmpty reports whether a report had no typed citation fields to audit.
func TypedCitationChannelsEmpty(typedCount, urlCount int) bool {
	return typedCount == 0 && urlCount == 0
}

// BuildCloseoutCitationGrounding records host-authored outcomes for typed closeout checks.
func BuildCloseoutCitationGrounding(
	roots evidence.CitationRoots,
	surfaceID string,
	report CoordinatorCompletionReport,
	ev CloseoutEvidence,
	eval CloseoutGroundingEval,
) *api.CitationGrounding {
	report.Normalize()
	if strings.TrimSpace(report.Synthesis) == "" {
		return nil
	}
	out := &api.CitationGrounding{}
	if eval.Code != "" {
		out.HintCode = eval.Code
		out.Checks = resolveCheckHandleTokens(ev.Ledger, ungroundedCitationChecks(eval.Code, eval.Offenders))
	} else {
		out.Traced = true
		out.Checks = resolveCheckHandleTokens(ev.Ledger, closeoutChecks(roots, surfaceID, report, eval, ev.Ledger))
	}
	FillObservedSamples(out, ev.Ledger)
	if len(report.CitedEvidence) > 0 {
		out.CitedEvidence = ResolveCitedEvidenceForWire(roots, ev.Ledger, report.CitedEvidence)
	}
	if len(report.CitedURLs) > 0 {
		out.CitedURLs = append([]string(nil), report.CitedURLs...)
	}
	FillProseSamples(out, eval.ProseDuplicateTokens, eval.ProseAdvisoryTokens)
	StampEvidenceRecords(out, ev.Ledger)
	if report.Verification != nil && report.Verification.Valid() {
		out.Verification = &api.CitationVerification{Method: report.Verification.Method, Reason: report.Verification.Reason}
	}
	if len(out.Checks) == 0 && len(out.CitedEvidence) == 0 && len(out.CitedURLs) == 0 && len(out.EvidenceRecords) == 0 {
		return nil
	}
	return out
}

// resolveCheckHandleTokens replaces handle tokens with their paths.
func resolveCheckHandleTokens(ev evidence.Ledger, checks []api.CitationGroundingCheck) []api.CitationGroundingCheck {
	for i := range checks {
		checks[i].Matched = evidence.ResolveHandleTokens(ev, checks[i].Matched)
		checks[i].Failed = evidence.ResolveHandleTokens(ev, checks[i].Failed)
	}
	return checks
}

func closeoutChecks(roots evidence.CitationRoots, surfaceID string, report CoordinatorCompletionReport, eval CloseoutGroundingEval, ev evidence.Ledger) []api.CitationGroundingCheck {
	var checks []api.CitationGroundingCheck
	if len(report.CitedEvidence) > 0 {
		matched := citedEvidenceCheckTokens(roots, ev, report.CitedEvidence)
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "cited_evidence",
			Label:   "Evidence citations",
			Status:  api.CitationGroundingCheckStatusPassed,
			Summary: fmt.Sprintf("%d citation(s) matched closeout evidence ledger", len(matched)),
			Matched: matched,
		})
	}
	if len(report.CitedURLs) > 0 {
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "url_citations",
			Label:   "URL citations",
			Status:  api.CitationGroundingCheckStatusPassed,
			Summary: fmt.Sprintf("%d URL(s) matched closeout evidence", len(report.CitedURLs)),
			Matched: append([]string(nil), report.CitedURLs...),
		})
	}
	if eval.BindAdvisoryCount > 0 {
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "bound_citations",
			Label:   "Host-bound citations",
			Status:  api.CitationGroundingCheckStatusPassed,
			Summary: fmt.Sprintf("%d loose citation(s) bound to a unique evidence record — grounded, host-added", eval.BindAdvisoryCount),
			Matched: append([]string(nil), eval.BindAdvisoryTokens...),
		})
	}
	if eval.SurveyAdvisoryCount > 0 {
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "survey_advisory",
			Label:   "Survey-altitude citations",
			Status:  api.CitationGroundingCheckStatusAdvisory,
			Summary: fmt.Sprintf("%d citation(s) at survey altitude (non-groundable) — advisory, closeout landed", eval.SurveyAdvisoryCount),
			Failed:  append([]string(nil), eval.SurveyAdvisoryTokens...),
		})
	}
	if eval.ProseDuplicateCount > 0 {
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "prose_duplication",
			Label:   "Closeout prose",
			Status:  api.CitationGroundingCheckStatusPassed,
			Summary: fmt.Sprintf("%d citation(s) duplicated in synthesis — already in typed fields", eval.ProseDuplicateCount),
			Matched: append([]string(nil), eval.ProseDuplicateTokens...),
		})
	}
	if TypedCitationChannelsEmpty(len(report.CitedEvidence), len(report.CitedURLs)) {
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "typed_citations",
			Label:   "Typed citations",
			Status:  api.CitationGroundingCheckStatusPassed,
			Vacuous: true,
			Summary: closeoutVacuousCitationSummary(surfaceID),
		})
	}
	if eval.ProseAdvisoryCount > 0 {
		checks = append(checks, api.CitationGroundingCheck{
			ID:      "prose_advisories",
			Label:   "Closeout prose",
			Status:  api.CitationGroundingCheckStatusAdvisory,
			Summary: fmt.Sprintf("%d citation(s) appeared only in synthesis prose — surfaced for review; prefer typed fields", eval.ProseAdvisoryCount),
			Failed:  append([]string(nil), eval.ProseAdvisoryTokens...),
		})
	}
	return stampCitationKind(checks)
}

func closeoutVacuousCitationSummary(surfaceID string) string {
	if strings.TrimSpace(surfaceID) == "implement_investigate" {
		return "No typed citations in investigate report"
	}
	return "No typed citations in synthesis report"
}

// ungroundedCitationChecks records typed citations missing from the ledger.
func ungroundedCitationChecks(code string, offenders []string) []api.CitationGroundingCheck {
	label, id := coordinatorCitationCheckMeta(code)
	return stampCitationKind([]api.CitationGroundingCheck{{
		ID:      id,
		Label:   label,
		Status:  api.CitationGroundingCheckStatusFailed,
		Summary: fmt.Sprintf("%d citation(s) didn't trace to tool evidence", len(offenders)),
		Failed:  append([]string(nil), offenders...),
	}})
}

func coordinatorCitationCheckMeta(code string) (label, id string) {
	switch code {
	case InvestHandleNotObservedCode, SynthHandleNotInLegsCode, InvestCitationUnverifiableCode, SynthCitationUnverifiableCode:
		return "Evidence citations", "cited_evidence"
	case InvestURLNotObservedCode, SynthURLNotObservedCode:
		return "URL citations", "url_citations"
	case InvestCitationsRequiredCode, SynthCitationsRequiredCode:
		return "Typed citations", "typed_citations"
	default:
		return "Typed citations", "typed_citations"
	}
}

// stampCitationKind marks closeout checks as citation checks.
func stampCitationKind(checks []api.CitationGroundingCheck) []api.CitationGroundingCheck {
	for i := range checks {
		checks[i].Kind = api.CitationGroundingCheckKindCitation
	}
	return checks
}
