package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
)

// PresentCloseoutMarkdown is the accepted display of a draft, derived by span
// from its parse. A resolved file reference becomes a path link at its validated
// coordinates. Handles without a path, control directives, unresolved references
// and damaged nodes are removed; their evidence presents through structured
// metadata. An observed link stays; an unvalidated one keeps only its label.
// Literal zones and surrounding prose are untouched.
func PresentCloseoutMarkdown(draft CloseoutDraft, resolved DraftCitationResolution) string {
	outcomes := make(map[int]OccurrenceResolution, len(resolved.Occurrences))
	for _, outcome := range resolved.Occurrences {
		outcomes[outcome.OccurrenceIndex] = outcome
	}
	return citationmd.Render(draft.Parsed, func(o citationmd.Occurrence) citationmd.Rendition {
		outcome, ok := outcomes[o.Index]
		accepted := ok && !outcome.Rejected()
		switch o.Kind {
		case citationmd.KindCitation:
			if path := presentablePath(outcome.Resolution.Path); accepted && path != "" {
				return citationmd.Rendition{Text: citationmd.FileLink(path, outcome.Resolution.Line, outcome.Resolution.EndLine)}
			}
		case citationmd.KindWebLink, citationmd.KindWorkspaceLink:
			if accepted {
				return citationmd.Rendition{Keep: true}
			}
			return citationmd.Rendition{Text: citationmd.PlainLabel(*o.Link)}
		}
		return citationmd.Rendition{}
	})
}

// presentablePath drops the search-root spelling a pathless observation records.
func presentablePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "." || path == "./" {
		return ""
	}
	return path
}
