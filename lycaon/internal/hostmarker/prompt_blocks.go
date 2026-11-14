package hostmarker

import "strings"

// Prompt blocks are host rows fixed to the prompt they precede; the row ID is
// the prefix followed by that prompt's message ID.
const (
	SourceChangesBlockPrefix = "host-source-changes-"
	ViewContextBlockPrefix   = "host-view-context-"
)

// PromptBlockAnchor returns the prompt a prompt block precedes.
func PromptBlockAnchor(id string) (string, bool) {
	for _, prefix := range []string{SourceChangesBlockPrefix, ViewContextBlockPrefix} {
		if anchor, ok := strings.CutPrefix(id, prefix); ok && anchor != "" {
			return anchor, true
		}
	}
	return "", false
}
