package guidance

import (
	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
)

// EvaluateDraftCitations folds occurrence outcomes into the closeout verdict.
// Unobserved references report before unverifiable ones and URLs, as they always
// have; an artifact that cannot be presented is an unobserved reference.
func EvaluateDraftCitations(draft CloseoutDraft, resolved DraftCitationResolution) CloseoutGroundingEval {
	owners := make(map[int]string, len(draft.Occurrences))
	tokens := make(map[int]string, len(draft.Occurrences))
	for _, occurrence := range draft.Occurrences {
		tokens[occurrence.Index] = occurrence.Token
		if occurrence.Owner != DraftFieldSynthesis {
			owners[occurrence.Index] = occurrence.Owner + ": "
		}
	}
	var notObserved, unverifiable, urls, survey []string
	var handleCode, unverifiableCode, urlCode string
	var bound []evidence.Resolution
	seen := map[string]struct{}{}
	add := func(list *[]string, offender string) {
		if _, dup := seen[offender]; !dup {
			seen[offender] = struct{}{}
			*list = append(*list, offender)
		}
	}
	for _, outcome := range resolved.Occurrences {
		offender := owners[outcome.OccurrenceIndex] + outcome.Offender
		switch {
		case outcome.Outcome == CitationSurvey:
			add(&survey, owners[outcome.OccurrenceIndex]+tokens[outcome.OccurrenceIndex])
		case outcome.Outcome == CitationGrounded:
			bound = append(bound, outcome.Resolution)
		case outcome.Outcome == ArtifactNotPresentable:
			add(&notObserved, "artifact:"+outcome.Offender)
			handleCode = outcome.Code
		case outcome.Kind == citationmd.KindWebLink && outcome.Rejected():
			add(&urls, outcome.Offender)
			urlCode = outcome.Code
		case outcome.Outcome == CitationNotObserved:
			add(&notObserved, offender)
			handleCode = outcome.Code
		case outcome.Outcome == CitationUnverifiable:
			add(&unverifiable, offender)
			unverifiableCode = outcome.Code
		case outcome.Outcome == DirectiveParsed, outcome.Outcome == ArtifactPresentable:
		}
	}
	switch {
	case len(notObserved) > 0:
		return CloseoutGroundingEval{Code: handleCode, Offenders: notObserved, UnobservedHandles: append([]string(nil), notObserved...)}
	case len(unverifiable) > 0:
		return CloseoutGroundingEval{Code: unverifiableCode, Offenders: unverifiable, CitationUnverifiable: true}
	case len(urls) > 0:
		return CloseoutGroundingEval{Code: urlCode, Offenders: urls, UnobservedURLs: append([]string(nil), urls...)}
	}
	boundTokens := BindAdvisoryTokens(bound)
	return CloseoutGroundingEval{
		BindAdvisoryTokens:   boundTokens,
		BindAdvisoryCount:    len(boundTokens),
		SurveyAdvisoryTokens: survey,
		SurveyAdvisoryCount:  len(survey),
	}
}

// AcceptedDraftReferences keeps the references that passed validation, bounded
// like the draft's own metadata.
func AcceptedDraftReferences(draft CloseoutDraft, resolved DraftCitationResolution) ([]CoordinatorCitedEvidence, []string, []string) {
	rejected := make(map[int]bool, len(resolved.Occurrences))
	for _, outcome := range resolved.Occurrences {
		rejected[outcome.OccurrenceIndex] = outcome.Rejected()
	}
	var cited []CoordinatorCitedEvidence
	var urls, artifacts []string
	for _, occurrence := range draft.Occurrences {
		if rejected[occurrence.Index] || occurrence.Owner != DraftFieldSynthesis {
			continue
		}
		switch occurrence.Kind {
		case citationmd.KindCitation, citationmd.KindWorkspaceLink:
			cited = append(cited, citedEvidenceFrom(*occurrence.Citation))
		case citationmd.KindWebLink:
			urls = append(urls, occurrence.Link.Destination)
		case citationmd.KindArtifact:
			artifacts = append(artifacts, occurrence.Artifact)
		case citationmd.KindVerify, citationmd.KindRepair:
		}
	}
	return normalizeCoordinatorCitedEvidence(cited, maxCoordinatorReportListItems),
		normalizeReportStringList(urls, maxCoordinatorReportListItems),
		normalizeReportStringList(artifacts, maxCoordinatorReportListItems)
}
