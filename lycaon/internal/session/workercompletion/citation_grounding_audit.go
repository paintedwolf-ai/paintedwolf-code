package workercompletion

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

type citationGroundingAudit struct {
	projectDir      string
	projectRoots    []projectroot.RootRef
	activeRootID    string
	ev              evidence.Ledger
	hints           *guidance.HintConfig
	checks          []api.CitationGroundingCheck
	proseDuplicates []string
	proseAdvisories []string
}

func newCitationGroundingAudit(ctx context.Context, in WorkerSummaryEvalInput) (citationGroundingAudit, error) {
	ev := evidence.Ledger{}
	if in.Ledger != nil && strings.TrimSpace(in.ChildSessionID) != "" {
		var err error
		ev, err = in.Ledger.LoadLedger(ctx, in.ChildSessionID)
		if err != nil {
			return citationGroundingAudit{}, err
		}
	}
	return citationGroundingAudit{
		projectDir:   strings.TrimSpace(in.ProjectDir),
		projectRoots: append([]projectroot.RootRef(nil), in.ProjectRoots...),
		activeRootID: strings.TrimSpace(in.ActiveRootID),
		ev:           ev,
		hints:        in.Hints,
	}, nil
}

func (a *citationGroundingAudit) finish(traced bool, hintCode string, report WorkerCompletionReport) api.CitationGrounding {
	out := api.CitationGrounding{
		Traced:   traced,
		HintCode: strings.TrimSpace(hintCode),
		Checks:   append([]api.CitationGroundingCheck(nil), a.checks...),
	}
	guidance.FillObservedSamples(&out, a.ev)
	if traced {
		out.Findings = resolvedFindingsWire(a.projectDir, a.projectRoots, a.activeRootID, a.ev, report.Findings)
		out.CitedURLs = append([]string(nil), report.CitedURLs...)
	}
	guidance.FillProseSamples(&out, a.proseDuplicates, a.proseAdvisories)
	guidance.StampEvidenceRecords(&out, a.ev)
	return out
}

func resolvedFindingsWire(projectDir string, roots []projectroot.RootRef, activeRootID string, ev evidence.Ledger, findings []WorkerFinding) []api.CitationGroundingFinding {
	if len(findings) == 0 {
		return nil
	}
	inputs := workerFindingsInput(findings)
	notes := make([]string, len(findings))
	for i, f := range findings {
		notes[i] = f.Note
	}
	citationRoots := evidence.CitationRoots{ProjectDir: projectDir, Roots: roots, ActiveRootID: activeRootID}
	resolved := guidance.ResolveFindingsForWire(citationRoots, ev, inputs, notes)
	out := make([]api.CitationGroundingFinding, 0, len(resolved))
	for _, item := range resolved {
		var openable *bool
		if len(roots) > 0 {
			openable = evidence.IsOpenablePath(citationRoots, item.Path)
		} else {
			openable = evidence.IsOpenablePath(evidence.CitationRoots{ProjectDir: projectDir}, item.Path)
		}
		out = append(out, api.CitationGroundingFinding{
			Handle:   item.Handle,
			Path:     item.Path,
			Line:     item.Line,
			Excerpt:  item.Excerpt,
			Verdict:  guidance.WireCitationVerdict(item.Verdict),
			Note:     item.Note,
			Openable: openable,
		})
	}
	return out
}

func (a *citationGroundingAudit) addCheck(check api.CitationGroundingCheck) {
	check.Matched = guidance.SampleGroundingStrings(evidence.ResolveHandleTokens(a.ev, check.Matched))
	check.Failed = guidance.SampleGroundingStrings(evidence.ResolveHandleTokens(a.ev, check.Failed))
	a.checks = append(a.checks, check)
}

// pass records a passed citation check that audited real citations.
func (a *citationGroundingAudit) pass(id, label, summary string, matched []string) {
	a.addCheck(api.CitationGroundingCheck{
		ID:      id,
		Label:   label,
		Status:  api.CitationGroundingCheckStatusPassed,
		Kind:    api.CitationGroundingCheckKindCitation,
		Summary: summary,
		Matched: matched,
	})
}

// passVacuous records a passed citation check that had nothing to verify.
func (a *citationGroundingAudit) passVacuous(id, label, summary string) {
	a.addCheck(api.CitationGroundingCheck{
		ID:      id,
		Label:   label,
		Status:  api.CitationGroundingCheckStatusPassed,
		Kind:    api.CitationGroundingCheckKindCitation,
		Vacuous: true,
		Summary: summary,
	})
}

// passLifecycle records a passed worker-lifecycle check (survey proof, disk artifact).
func (a *citationGroundingAudit) passLifecycle(id, label, summary string) {
	a.addCheck(api.CitationGroundingCheck{
		ID:      id,
		Label:   label,
		Status:  api.CitationGroundingCheckStatusPassed,
		Kind:    api.CitationGroundingCheckKindLifecycle,
		Summary: summary,
	})
}

func (a *citationGroundingAudit) recordTypedCitationCheck(eval guidance.WorkerCitationEval) (offenders []string, ok bool) {
	label, id := a.typedCitationCheckMeta(eval.Code)
	switch {
	case len(eval.Offenders) > 0:
		summary := fmt.Sprintf("%d typed citation(s) failed ledger check", len(eval.Offenders))
		a.addCheck(api.CitationGroundingCheck{
			ID:      id,
			Label:   label,
			Status:  api.CitationGroundingCheckStatusFailed,
			Kind:    api.CitationGroundingCheckKindCitation,
			Summary: summary,
			Failed:  eval.Offenders,
		})
		return eval.Offenders, false
	default:
		a.pass(id, label, "All typed citations matched tool evidence", nil)
	}
	return nil, true
}

// typedCitationCheckMeta resolves the UI label and check id for an evidence code
// from the hint registry, falling back to a humanized code when undeclared.
func (a *citationGroundingAudit) typedCitationCheckMeta(code string) (label, id string) {
	if label, id, ok := a.hints.EvidenceCheckMeta(code); ok && label != "" && id != "" {
		return label, id
	}
	return "Typed citations", "typed_citations"
}

func (a *citationGroundingAudit) recordFindingCitationVerdicts(eval guidance.WorkerCitationEval) {
	if len(eval.Resolutions) == 0 {
		return
	}
	label, id := a.tracedCitationCheckMeta()
	var traced, matched []string
	for _, res := range eval.Resolutions {
		token := guidance.FormatResolutionOffender(res, guidance.WorkerFindingInput{
			Evidence: res.Handle,
			Line:     res.Line,
			Excerpt:  res.Excerpt,
		})
		switch res.Verdict {
		case evidence.VerdictTraced:
			traced = append(traced, token)
		case evidence.VerdictMatched:
			matched = append(matched, token)
		case evidence.VerdictUnverifiable, evidence.VerdictBound, evidence.VerdictAmbiguous:
		}
	}
	if len(traced) > 0 {
		a.addCheck(api.CitationGroundingCheck{
			ID:      id,
			Label:   label,
			Status:  api.CitationGroundingCheckStatusAdvisory,
			Kind:    api.CitationGroundingCheckKindCitation,
			Summary: fmt.Sprintf("%d cited excerpt(s) weren't found word-for-word in the captured source; the cited path was observed — shown for review", len(traced)),
			Failed:  traced,
			Matched: matched,
		})
		return
	}
	if len(matched) > 0 {
		a.pass(id, label, fmt.Sprintf("%d finding excerpt(s) matched tool evidence", len(matched)), matched)
	}
}

func (a *citationGroundingAudit) tracedCitationCheckMeta() (label, id string) {
	if label, id, ok := a.hints.EvidenceCheckMeta(guidance.WorkerExcerptHandleMismatchCode); ok && label != "" && id != "" {
		return label, id
	}
	return "Finding excerpts", "finding_excerpts"
}

func (a *citationGroundingAudit) recordTypedCitationChannels(report WorkerCompletionReport) {
	if len(report.CitedURLs) > 0 {
		a.pass("url_citations", "URL citations", fmt.Sprintf("%d URL(s) matched web tool evidence", len(report.CitedURLs)), report.CitedURLs)
	}
	if guidance.TypedCitationChannelsEmpty(len(workerReportCitations(report)), len(report.CitedURLs)) {
		a.passVacuous("typed_citations", "Typed citations", "No typed citations in report")
	}
}

func (a *citationGroundingAudit) recordProseDuplication(eval guidance.WorkerCitationEval) {
	if eval.ProseDuplicateCount <= 0 {
		return
	}
	a.proseDuplicates = eval.ProseDuplicateTokens
	a.pass("prose_duplication", "Narrative prose", fmt.Sprintf("%d citation(s) duplicated in narrative — already in typed fields", eval.ProseDuplicateCount), eval.ProseDuplicateTokens)
}

func (a *citationGroundingAudit) recordBindAdvisories(eval guidance.WorkerCitationEval) {
	if eval.BindAdvisoryCount <= 0 {
		return
	}
	a.addCheck(api.CitationGroundingCheck{
		ID:      "bound_citations",
		Label:   "Host-bound citations",
		Status:  api.CitationGroundingCheckStatusPassed,
		Kind:    api.CitationGroundingCheckKindCitation,
		Summary: fmt.Sprintf("%d loose citation(s) bound to a unique evidence record — grounded, host-added", eval.BindAdvisoryCount),
		Matched: guidance.SampleGroundingStrings(eval.BindAdvisoryTokens),
	})
}

// recordSurveyAdvisories records survey-grade citation placement.
func (a *citationGroundingAudit) recordSurveyAdvisories(eval guidance.WorkerCitationEval) {
	if eval.SurveyAdvisoryCount <= 0 {
		return
	}
	a.addCheck(api.CitationGroundingCheck{
		ID:      "survey_advisory",
		Label:   "Survey-altitude citations",
		Status:  api.CitationGroundingCheckStatusAdvisory,
		Kind:    api.CitationGroundingCheckKindCitation,
		Summary: fmt.Sprintf("%d citation(s) at survey altitude (non-groundable) — advisory, surfaced for review", eval.SurveyAdvisoryCount),
		Failed:  guidance.SampleGroundingStrings(eval.SurveyAdvisoryTokens),
	})
}

func (a *citationGroundingAudit) recordProseAdvisories(eval guidance.WorkerCitationEval) {
	if eval.ProseAdvisoryCount <= 0 {
		return
	}
	a.proseAdvisories = eval.ProseAdvisoryTokens
	a.addCheck(api.CitationGroundingCheck{
		ID:      "prose_advisories",
		Label:   "Narrative prose",
		Status:  api.CitationGroundingCheckStatusAdvisory,
		Kind:    api.CitationGroundingCheckKindCitation,
		Summary: fmt.Sprintf("%d citation(s) in narrative — surfaced for review; prefer typed fields", eval.ProseAdvisoryCount),
		Matched: guidance.SampleGroundingStrings(eval.ProseAdvisoryTokens),
	})
}

func (a *citationGroundingAudit) recordLineCorrections(count int) {
	if count <= 0 {
		return
	}
	a.addCheck(api.CitationGroundingCheck{
		ID:      "line_corrections",
		Label:   "Line corrections",
		Status:  api.CitationGroundingCheckStatusAdvisory,
		Kind:    api.CitationGroundingCheckKindCitation,
		Summary: fmt.Sprintf("%d citation line(s) corrected to the observed line", count),
	})
}

func (a *citationGroundingAudit) recordScoutSurveyCheck() (ok bool) {
	if guidance.LedgerHasSurveyHandle(a.ev) {
		a.passLifecycle("scout_survey", "Survey tool activity", "At least one successful survey handle in leg evidence")
		return true
	}
	a.addCheck(api.CitationGroundingCheck{
		ID:      "scout_survey",
		Label:   "Survey tool activity",
		Status:  api.CitationGroundingCheckStatusFailed,
		Kind:    api.CitationGroundingCheckKindLifecycle,
		Summary: "No survey-kind handles in leg evidence",
	})
	return false
}

// recordImplementerArtifactCheck records the artifact probe and reports
// whether the worker clears the gate. An undetermined probe records as
// advisory and does not reject: absence was never established.
func (a *citationGroundingAudit) recordImplementerArtifactCheck(proof artifactProof) (ok bool) {
	switch proof {
	case artifactPresent:
		a.passLifecycle("implementer_artifact", "Workspace artifact", "Mutation tools or workspace changes recorded")
		return true
	case artifactUndetermined:
		a.addCheck(api.CitationGroundingCheck{
			ID:      "implementer_artifact",
			Label:   "Workspace artifact",
			Status:  api.CitationGroundingCheckStatusAdvisory,
			Kind:    api.CitationGroundingCheckKindLifecycle,
			Summary: "Workspace probe could not run — artifact neither confirmed nor ruled out",
		})
		return true
	default:
		a.addCheck(api.CitationGroundingCheck{
			ID:      "implementer_artifact",
			Label:   "Workspace artifact",
			Status:  api.CitationGroundingCheckStatusFailed,
			Kind:    api.CitationGroundingCheckKindLifecycle,
			Summary: "No mutation tool activity or workspace changes detected",
		})
		return false
	}
}

// CitationGroundingWire returns a heap copy for JSON wire fields when audit data exists.
func CitationGroundingWire(src *api.CitationGrounding) *api.CitationGrounding {
	if src == nil || len(src.Checks) == 0 && !src.Traced && strings.TrimSpace(src.HintCode) == "" {
		return nil
	}
	out := *src
	return &out
}
