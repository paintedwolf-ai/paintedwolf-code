package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
)

// CitationOutcome is the resolver's decision for one occurrence.
type CitationOutcome string

const (
	// CitationGrounded means the source was observed and the coordinates are
	// supported. It does not prove the surrounding prose.
	CitationGrounded CitationOutcome = "grounded"
	// CitationSurvey is a survey-grade record, valid for existence only.
	CitationSurvey CitationOutcome = "survey"
	// CitationNotObserved means nothing in the ledger matches.
	CitationNotObserved CitationOutcome = "not_observed"
	// CitationUnverifiable means the source was observed but does not support
	// the coordinates, or more than one record matches.
	CitationUnverifiable CitationOutcome = "unverifiable"
	// ArtifactPresentable and ArtifactNotPresentable decide an artifact directive.
	ArtifactPresentable    CitationOutcome = "artifact_presentable"
	ArtifactNotPresentable CitationOutcome = "artifact_not_presentable"
	// DirectiveParsed marks a directive with nothing to resolve here. A repair
	// directive's validity depends on the retained draft.
	DirectiveParsed CitationOutcome = "directive"
)

// OccurrenceResolution is one occurrence's outcome. OccurrenceIndex is the
// occurrence's stable index in the draft. Code is set on a rejected citation.
type OccurrenceResolution struct {
	OccurrenceIndex int
	Kind            citationmd.NodeKind
	Outcome         CitationOutcome
	Code            string
	Offender        string
	Resolution      evidence.Resolution
	// ObservedURL is the URL a web link matched in the ledger.
	ObservedURL string
}

// Rejected reports whether this occurrence fails supplied-reference validation.
func (r OccurrenceResolution) Rejected() bool {
	switch r.Outcome {
	case CitationNotObserved, CitationUnverifiable, ArtifactNotPresentable:
		return true
	default:
		return false
	}
}

// DraftCitationResolution holds an outcome for every occurrence, in order, and
// the parse issues that also block acceptance.
type DraftCitationResolution struct {
	Occurrences []OccurrenceResolution
	Issues      []citationmd.Issue
}

// Rejected lists the occurrences that failed validation.
func (r DraftCitationResolution) Rejected() []OccurrenceResolution {
	var out []OccurrenceResolution
	for _, occurrence := range r.Occurrences {
		if occurrence.Rejected() {
			out = append(out, occurrence)
		}
	}
	return out
}

// ResolveDraftCitations validates every occurrence of a draft, not its
// deduplicated metadata. presentable lists the artifacts this turn may present.
// ev must be loaded evidence: a caller fails on a ledger read error before calling,
// because an empty ledger here means nothing was observed.
func ResolveDraftCitations(
	roots evidence.CitationRoots,
	surfaceID string,
	draft CloseoutDraft,
	ev CloseoutEvidence,
	presentable []string,
) DraftCitationResolution {
	handleCode, urlCode := closeoutCitationRejectCodes(surfaceID)
	unverifiableCode := closeoutCitationUnverifiableCode(surfaceID)
	out := DraftCitationResolution{Issues: draft.Issues, Occurrences: make([]OccurrenceResolution, 0, len(draft.Occurrences))}
	for _, occurrence := range draft.Occurrences {
		result := OccurrenceResolution{OccurrenceIndex: occurrence.Index, Kind: occurrence.Kind, Outcome: DirectiveParsed}
		switch occurrence.Kind {
		case citationmd.KindCitation, citationmd.KindWorkspaceLink:
			result = resolveDraftCitation(roots, occurrence, ev.Ledger, handleCode, unverifiableCode)
		case citationmd.KindWebLink:
			result.Outcome = CitationGrounded
			observed := observedWebURL(*occurrence.Link, ev.Ledger)
			result.ObservedURL = observed
			if observed == "" {
				result.Outcome, result.Code, result.Offender = CitationNotObserved, urlCode, occurrence.Link.Destination
			}
		case citationmd.KindArtifact:
			result.Outcome = ArtifactNotPresentable
			result.Offender = occurrence.Artifact
			for _, id := range presentable {
				if strings.EqualFold(strings.TrimSpace(id), occurrence.Artifact) {
					result.Outcome, result.Offender = ArtifactPresentable, ""
					break
				}
			}
		case citationmd.KindVerify, citationmd.KindRepair:
		}
		out.Occurrences = append(out.Occurrences, result)
	}
	return out
}

// observedWebURL finds the observed URL a link names, or "". A schemeless `www.`
// link matches at either scheme, since it names neither.
func observedWebURL(link citationmd.Link, ev evidence.Ledger) string {
	destination := strings.TrimSpace(link.Destination)
	if ev.URLSeen(destination) {
		return destination
	}
	if rest, ok := strings.CutPrefix(destination, "http://"); ok && strings.HasPrefix(strings.ToLower(link.Label), "www.") {
		if observed := "https://" + rest; ev.URLSeen(observed) {
			return observed
		}
	}
	return ""
}

func resolveDraftCitation(roots evidence.CitationRoots, occurrence DraftOccurrence, ev evidence.Ledger, handleCode, unverifiableCode string) OccurrenceResolution {
	c := occurrence.Citation
	finding := WorkerFindingInput{Evidence: c.Handle, Path: c.Path, Line: c.Line, EndLine: c.EndLine, Excerpt: c.Excerpt}
	result := OccurrenceResolution{OccurrenceIndex: occurrence.Index, Kind: occurrence.Kind}
	outcome := classifyDraftFinding(roots, finding, ev)
	result.Resolution, result.Offender = outcome.resolution, outcome.offender
	switch outcome.class {
	case closeoutCitationSurvey:
		result.Outcome = CitationSurvey
		result.Offender = ""
	case closeoutCitationNotObserved:
		result.Outcome, result.Code = CitationNotObserved, handleCode
	case closeoutCitationUnverifiable:
		result.Outcome, result.Code = CitationUnverifiable, unverifiableCode
	default:
		result.Outcome = CitationGrounded
		result.Offender = ""
	}
	return result
}

// classifyDraftCoordinate validates a coordinate against what the record observed.
// One line is a one-line range, so dropping the end line cannot skip the check. A
// record with no line facts for the location supports no coordinate.
func classifyDraftCoordinate(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger) closeoutFindingOutcome {
	single := finding.EndLine == 0
	if single {
		finding.EndLine = finding.Line
	}
	outcome := classifyCloseoutFinding(roots, finding, ev)
	if single && outcome.resolution.EndLine == outcome.resolution.Line {
		outcome.resolution.EndLine = 0
	}
	return outcome
}

// classifyDraftFinding binds a bare path to an eligible observed record, preferring
// content over survey. A Markdown path citation names a file and needs no line.
func classifyDraftFinding(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger) closeoutFindingOutcome {
	if finding.Line > 0 {
		return classifyDraftCoordinate(roots, finding, ev)
	}
	if finding.Evidence != "" || finding.Path == "" || finding.Excerpt != "" {
		return classifyCloseoutFinding(roots, finding, ev)
	}
	key := evidence.LedgerPathKey(roots, finding.Path, ev.ByPath)
	handles := ev.ByPath[key]
	for i := len(handles) - 1; i >= 0; i-- {
		if rec, ok := evidence.ResolveHandle(ev, handles[i]); ok && rec.SupersededBy == "" && !rec.IsGate() && !rec.Survey {
			return closeoutFindingOutcome{class: closeoutCitationGrounded, resolved: true,
				resolution: evidence.Resolution{Handle: handles[i], Path: key, Verdict: evidence.VerdictTraced}}
		}
	}
	if citationResolvesToSurveyRecord(roots, finding, ev) {
		return closeoutFindingOutcome{class: closeoutCitationSurvey, offender: finding.Path, resolved: true,
			resolution: evidence.Resolution{Path: key, Verdict: evidence.VerdictTraced}}
	}
	class := closeoutCitationNotObserved
	if len(handles) > 0 {
		class = closeoutCitationUnverifiable
	}
	return closeoutFindingOutcome{class: class, offender: finding.Path}
}
