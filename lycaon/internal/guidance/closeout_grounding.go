package guidance

import (
	"slices"
	"strconv"
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
	CitationRepairInvalidCode      = "CITATION_REPAIR_INVALID"
	CitationSyntaxInvalidCode      = "CITATION_SYNTAX_INVALID"
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

// CloseoutCitationsSubsetOfPrior compares resolved citations with the prior
// closeout's, both in the form stored on grounding, so a handle and the path it
// resolved to are one citation.
func CloseoutCitationsSubsetOfPrior(history []api.Message, current *api.CitationGrounding) (bool, []string) {
	prior := lastCommittedCloseoutGrounding(history)
	if prior == nil {
		return false, nil
	}
	tokens := citationTokenSetFromGrounding(current)
	if len(tokens) == 0 {
		return false, nil
	}
	priorSet := citationTokenSetFromGrounding(prior)
	if len(priorSet) == 0 {
		return false, nil
	}
	var offenders []string
	for tok := range tokens {
		if _, ok := priorSet[tok]; !ok {
			return false, nil
		}
		offenders = append(offenders, tok)
	}
	slices.Sort(offenders)
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
		if tok := citationToken(c.Handle, c.Path, c.Line, c.EndLine); tok != "" {
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

func citationToken(handle, path string, line, endLine int) string {
	var name string
	if p := strings.TrimSpace(path); p != "" {
		name = "path:" + p
	} else if h := strings.TrimSpace(handle); h != "" {
		name = "handle:" + h
	} else {
		return ""
	}
	if line > 0 {
		name += ":" + strconv.Itoa(line)
	}
	if endLine > line {
		name += "-" + strconv.Itoa(endLine)
	}
	return name
}

// CloseoutGroundingEval is the typed closeout citation result.
type CloseoutGroundingEval struct {
	Code                 string
	Offenders            []string
	UnobservedHandles    []string
	UnobservedURLs       []string
	CitationUnverifiable bool
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
	bound := BindAdvisoryTokens(resolved)
	return CloseoutGroundingEval{
		BindAdvisoryTokens:   bound,
		BindAdvisoryCount:    len(bound),
		SurveyAdvisoryTokens: survey,
		SurveyAdvisoryCount:  len(survey),
	}
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
	if norm, ok := normalizeSingleCoordinatorCitedEvidence(item); ok {
		item = norm
	}
	return WorkerFindingInput{Evidence: item.Evidence, Path: item.Path, Line: item.Line, EndLine: item.EndLine, Excerpt: item.Excerpt}
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
			EndLine:  item.EndLine,
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
