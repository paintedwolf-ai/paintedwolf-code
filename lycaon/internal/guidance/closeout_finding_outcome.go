package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// closeoutCitationClass is the resolver's decision for one citation.
type closeoutCitationClass int

const (
	closeoutCitationGrounded closeoutCitationClass = iota
	closeoutCitationSurvey
	closeoutCitationNotObserved
	closeoutCitationUnverifiable
	closeoutCitationEmpty
)

// closeoutFindingOutcome carries one citation's class and, where resolution ran,
// its result. offender is the reject or advisory token.
type closeoutFindingOutcome struct {
	class      closeoutCitationClass
	offender   string
	resolution evidence.Resolution
	resolved   bool
}

// classifyCloseoutFinding separates missing, unverifiable and survey citations.
func classifyCloseoutFinding(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger) closeoutFindingOutcome {
	path := strings.TrimSpace(finding.Path)
	handle := strings.TrimSpace(finding.Evidence)
	if path == "" && handle == "" {
		return closeoutFindingOutcome{class: closeoutCitationEmpty}
	}
	line := finding.Line
	excerpt := strings.TrimSpace(finding.Excerpt)
	// A range needs ordered positive bounds before anything can support it.
	if finding.EndLine > 0 && (line <= 0 || finding.EndLine < line) {
		return closeoutFindingOutcome{class: closeoutCitationUnverifiable, offender: formatFindingOffender(finding)}
	}
	if handle == "" && path != "" && line <= 0 && excerpt == "" {
		return classifyBareCloseoutPath(roots, finding, ev, path)
	}
	// Bare handles still require ledger membership.
	if line <= 0 && excerpt == "" {
		return classifyBareCloseoutHandle(roots, finding, ev, handle, path)
	}
	if citationResolvesToVisualIntent(finding, ev) {
		token := handle
		if token == "" {
			token = path
		}
		return closeoutFindingOutcome{class: closeoutCitationUnverifiable, offender: token}
	}
	res := resolveWorkerFindingCitation(roots, finding, ev)
	outcome := closeoutFindingOutcome{resolution: res, resolved: true, offender: FormatResolutionOffender(res, finding)}
	switch {
	case res.Verdict.Grounded():
		outcome.class = closeoutCitationGrounded
	case citationResolvesToSurveyRecord(roots, finding, ev):
		outcome.class = closeoutCitationSurvey
	case citationWasObserved(roots, finding, ev), finding.EndLine > 0 && len(ev.ByPath[res.Path]) > 0:
		// A loose citation may name no path, yet its range failed on a record.
		outcome.class = closeoutCitationUnverifiable
	default:
		outcome.class = closeoutCitationNotObserved
	}
	return outcome
}

func classifyBareCloseoutPath(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger, path string) closeoutFindingOutcome {
	switch {
	case citationResolvesToSurveyRecord(roots, finding, ev):
		return closeoutFindingOutcome{class: closeoutCitationSurvey, offender: path}
	case citationWasObserved(roots, finding, ev):
		return closeoutFindingOutcome{class: closeoutCitationUnverifiable, offender: path}
	default:
		return closeoutFindingOutcome{class: closeoutCitationNotObserved, offender: path}
	}
}

func classifyBareCloseoutHandle(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger, handle, path string) closeoutFindingOutcome {
	res, found := resolveBareHandleCitation(roots, ev, handle, path)
	outcome := closeoutFindingOutcome{resolution: res, resolved: true}
	switch {
	case !found:
		outcome.offender = FormatResolutionOffender(res, finding)
		outcome.class = closeoutCitationNotObserved
		if _, ok := evidence.ResolveHandle(ev, handle); ok {
			outcome.class = closeoutCitationUnverifiable
		}
	case citationResolvesToSurveyRecord(roots, finding, ev):
		outcome.class, outcome.offender = closeoutCitationSurvey, handle
	default:
		outcome.class = closeoutCitationGrounded
	}
	return outcome
}
