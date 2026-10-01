package guidance

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	SynthURLNotObservedCode        = "SYNTH_URL_NOT_OBSERVED"
	SynthHandleNotInLegsCode       = "SYNTH_HANDLE_NOT_IN_LEGS"
	SynthCitationsRequiredCode     = "SYNTH_CITATIONS_REQUIRED"
	SynthCitationUnverifiableCode  = "SYNTH_CITATION_UNVERIFIABLE"
	SynthNoNewEvidenceCode         = "SYNTH_NO_NEW_EVIDENCE"
	InvestHandleNotObservedCode    = "INVEST_HANDLE_NOT_OBSERVED"
	InvestURLNotObservedCode       = "INVEST_URL_NOT_OBSERVED"
	InvestCitationsRequiredCode    = "INVEST_CITATIONS_REQUIRED"
	InvestCitationUnverifiableCode = "INVEST_CITATION_UNVERIFIABLE"
	InvestNoNewEvidenceCode        = "INVEST_NO_NEW_EVIDENCE"
)

// CloseoutLedgerHasCitableEvidence excludes gate records.
func CloseoutLedgerHasCitableEvidence(ev CloseoutEvidence) bool {
	for handle, rec := range ev.Handles {
		if strings.TrimSpace(handle) != "" && !rec.IsGate() {
			return true
		}
	}
	return false
}

// ObservedSampleCap bounds citations attached during host assembly.
const ObservedSampleCap = 12

// BindObservedSample attaches a bounded sample of observed citations.
func BindObservedSample(report CoordinatorCompletionReport, ev evidence.Ledger, roots evidence.CitationRoots) CoordinatorCompletionReport {
	existingURLs := map[string]struct{}{}
	for _, u := range report.CitedURLs {
		existingURLs[u] = struct{}{}
	}
	report.CitedURLs = append(report.CitedURLs, SelectObservedURLSample(ev, report.Synthesis, existingURLs)...)

	existingPaths := map[string]struct{}{}
	for _, c := range report.CitedEvidence {
		existingPaths[c.Path] = struct{}{}
	}
	for _, sample := range SelectObservedPathSample(ev, roots, report.Synthesis, existingPaths) {
		report.CitedEvidence = append(report.CitedEvidence, CoordinatorCitedEvidence{
			Path: sample.Path,
			Line: sample.Line,
		})
	}
	report.Normalize()
	return report
}

func CloseoutCitationsRequiredCode(surfaceID string) string {
	if strings.TrimSpace(surfaceID) == "implement_investigate" {
		return InvestCitationsRequiredCode
	}
	return SynthCitationsRequiredCode
}

// CloseoutNoNewEvidenceCode identifies citation reuse within an intent window.
func CloseoutNoNewEvidenceCode(surfaceID string) string {
	if strings.TrimSpace(surfaceID) == "implement_investigate" {
		return InvestNoNewEvidenceCode
	}
	return SynthNoNewEvidenceCode
}

// CloseoutCitationsSubsetOfPrior checks typed citations against the prior closeout.
func CloseoutCitationsSubsetOfPrior(history []api.Message, report CoordinatorCompletionReport) (bool, []string) {
	prior := lastCommittedCloseoutGrounding(history)
	if prior == nil {
		return false, nil
	}
	current := citationTokenSetFromReport(report)
	if len(current) == 0 {
		return false, nil
	}
	priorSet := citationTokenSetFromGrounding(prior)
	if len(priorSet) == 0 {
		return false, nil
	}
	var offenders []string
	for tok := range current {
		if _, ok := priorSet[tok]; !ok {
			return false, nil
		}
		offenders = append(offenders, tok)
	}
	return true, offenders
}

func lastCommittedCloseoutGrounding(history []api.Message) *api.CitationGrounding {
	since := api.UserIntentBoundary(history)
	if since < 0 {
		since = 0
	}
	for i := len(history) - 1; i >= since; i-- {
		m := history[i]
		if m.Role != api.MessageRoleAssistant {
			continue
		}
		if m.Visibility != api.MessageVisibilityTranscript {
			continue
		}
		if api.IsAgentNoteMessage(m) {
			continue
		}
		if m.Grounding != nil {
			return m.Grounding
		}
	}
	return nil
}

func citationTokenSetFromGrounding(g *api.CitationGrounding) map[string]struct{} {
	out := map[string]struct{}{}
	if g == nil {
		return out
	}
	for _, c := range g.CitedEvidence {
		if tok := citationToken(c.Handle, c.Path, c.Line); tok != "" {
			out[tok] = struct{}{}
		}
	}
	for _, u := range g.CitedURLs {
		if u = strings.TrimSpace(u); u != "" {
			out["url:"+u] = struct{}{}
		}
	}
	return out
}

func citationTokenSetFromReport(report CoordinatorCompletionReport) map[string]struct{} {
	out := map[string]struct{}{}
	for _, c := range report.CitedEvidence {
		if tok := citationToken(c.Evidence, c.Path, c.Line); tok != "" {
			out[tok] = struct{}{}
		}
	}
	for _, u := range report.CitedURLs {
		if u = strings.TrimSpace(u); u != "" {
			out["url:"+u] = struct{}{}
		}
	}
	return out
}

func citationToken(handle, path string, line int) string {
	if p := strings.TrimSpace(path); p != "" {
		if line > 0 {
			return fmt.Sprintf("path:%s:%d", p, line)
		}
		return "path:" + p
	}
	if h := strings.TrimSpace(handle); h != "" {
		if line > 0 {
			return fmt.Sprintf("handle:%s:%d", h, line)
		}
		return "handle:" + h
	}
	return ""
}

// CloseoutGroundingEval is the typed closeout citation result.
type CloseoutGroundingEval struct {
	Code                 string
	Offenders            []string
	UnobservedHandles    []string
	UnobservedURLs       []string
	CitationUnverifiable bool
	ProseDuplicateTokens []string
	ProseDuplicateCount  int
	ProseAdvisoryTokens  []string
	ProseAdvisoryCount   int
	BindAdvisoryTokens   []string
	BindAdvisoryCount    int
	SurveyAdvisoryTokens []string
	SurveyAdvisoryCount  int
}

func closeoutCitationRejectCodes(surfaceID string) (handleCode, urlCode string) {
	if strings.TrimSpace(surfaceID) == "implement_investigate" {
		return InvestHandleNotObservedCode, InvestURLNotObservedCode
	}
	return SynthHandleNotInLegsCode, SynthURLNotObservedCode
}

func closeoutCitationUnverifiableCode(surfaceID string) string {
	if strings.TrimSpace(surfaceID) == "implement_investigate" {
		return InvestCitationUnverifiableCode
	}
	return SynthCitationUnverifiableCode
}

// EvaluateCloseoutCitations checks typed fields against merged evidence.
func EvaluateCloseoutCitations(roots evidence.CitationRoots, surfaceID string, report CoordinatorCompletionReport, ev CloseoutEvidence) CloseoutGroundingEval {
	handleCode, urlCode := closeoutCitationRejectCodes(surfaceID)
	report.Normalize()
	inputs := coordinatorCitedEvidenceInputs(report.CitedEvidence)
	resolved, notObserved, unverifiable, survey := resolveCloseoutFindingCitations(roots, inputs, ev.Ledger)
	if len(notObserved) > 0 {
		return CloseoutGroundingEval{
			Code:              handleCode,
			Offenders:         notObserved,
			UnobservedHandles: append([]string(nil), notObserved...),
		}
	}
	if len(unverifiable) > 0 {
		return CloseoutGroundingEval{
			Code:                 closeoutCitationUnverifiableCode(surfaceID),
			Offenders:            unverifiable,
			CitationUnverifiable: true,
		}
	}
	if missing := citedURLHandleOffenders(report.CitedURLs, ev.Ledger); len(missing) > 0 {
		return CloseoutGroundingEval{
			Code:           urlCode,
			Offenders:      missing,
			UnobservedURLs: append([]string(nil), missing...),
		}
	}
	typed := buildCloseoutTypedChannel(roots, ev, report.CitedEvidence, report.CitedURLs)
	proseEval := EvaluateWorkerProseLeaks(WorkerNarrativeInput{Brief: report.Synthesis}, ev.Ledger, typed)
	bound := BindAdvisoryTokens(resolved)
	return CloseoutGroundingEval{
		ProseDuplicateTokens: proseEval.DuplicateTokens,
		ProseDuplicateCount:  len(proseEval.DuplicateTokens),
		ProseAdvisoryTokens:  proseEval.AdvisoryLeakTokens,
		ProseAdvisoryCount:   len(proseEval.AdvisoryLeakTokens),
		BindAdvisoryTokens:   bound,
		BindAdvisoryCount:    len(bound),
		SurveyAdvisoryTokens: survey,
		SurveyAdvisoryCount:  len(survey),
	}
}

func buildCloseoutTypedChannel(roots evidence.CitationRoots, ev CloseoutEvidence, citedEvidence []CoordinatorCitedEvidence, citedURLs []string) TypedChannel {
	inputs := coordinatorCitedEvidenceInputs(citedEvidence)
	return BuildWorkerTypedChannel(roots, ev.Ledger, inputs, citedURLs)
}

func coordinatorCitedEvidenceInputs(cited []CoordinatorCitedEvidence) []WorkerFindingInput {
	if len(cited) == 0 {
		return nil
	}
	out := make([]WorkerFindingInput, 0, len(cited))
	for _, item := range cited {
		out = append(out, coordinatorFindingFromCitedEvidence(item))
	}
	return out
}

func coordinatorFindingFromCitedEvidence(item CoordinatorCitedEvidence) WorkerFindingInput {
	return WorkerFindingInput{
		Evidence: item.Evidence,
		Path:     item.Path,
		Line:     item.Line,
		Excerpt:  item.Excerpt,
	}
}

// ResolveCitedEvidenceForWire maps typed citations to host provenance.
func ResolveCitedEvidenceForWire(roots evidence.CitationRoots, ev evidence.Ledger, cited []CoordinatorCitedEvidence) []api.CitationGroundingCitedEvidence {
	resolved := ResolveFindingsForWire(roots, ev, coordinatorCitedEvidenceInputs(cited), nil)
	if len(resolved) == 0 {
		return nil
	}
	out := make([]api.CitationGroundingCitedEvidence, 0, len(resolved))
	for _, item := range resolved {
		out = append(out, api.CitationGroundingCitedEvidence{
			Handle:   item.Handle,
			Path:     item.Path,
			Line:     item.Line,
			Excerpt:  item.Excerpt,
			Verdict:  WireCitationVerdict(item.Verdict),
			Openable: evidence.IsOpenablePath(roots, item.Path),
		})
	}
	return out
}

func citedEvidenceCheckTokens(roots evidence.CitationRoots, ev evidence.Ledger, cited []CoordinatorCitedEvidence) []string {
	inputs := coordinatorCitedEvidenceInputs(cited)
	var out []string
	for _, finding := range inputs {
		res := resolveWorkerFindingCitation(roots, finding, ev)
		if !res.Verdict.Grounded() {
			continue
		}
		out = append(out, FormatResolutionOffender(res, finding))
	}
	return out
}
