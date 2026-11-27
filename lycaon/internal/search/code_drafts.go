package search

import (
	"context"
	"strings"
)

// codeDraftOverlayMaxDocs bounds how many dirty editor drafts are projected
// for a single searched root in one code-search leg.
const codeDraftOverlayMaxDocs = 256

// CodeDraft is an authorized caller's unsaved editor buffer for one path
// inside a searched project root.
type CodeDraft struct {
	Path    string
	Content string
}

// CodeDraftProvider returns bounded active dirty editor drafts visible to ctx
// for one (projectID, rootID) pair. Implementations must derive caller and
// workspace authorization from ctx/request rather than trusting IDs alone.
type CodeDraftProvider func(ctx context.Context, projectID, rootID string, maxDocs int, maxBytes int64) ([]CodeDraft, error)

// validExactDraftRelPath validates a root-relative POSIX path without trimming
// whitespace or rewriting literal backslashes, preserving exact filename
// identity so distinct POSIX filenames cannot collide.
func validExactDraftRelPath(rel string) bool {
	if rel == "" || strings.ContainsRune(rel, '\x00') || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
