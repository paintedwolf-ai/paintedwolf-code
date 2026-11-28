package search

import (
	"context"
	"strings"
)

const (
	// codeDraftOverlayMaxDocs bounds how many dirty editor drafts are
	// materialized for a single searched root in one code-search leg.
	codeDraftOverlayMaxDocs = 256
	// codeDraftOverlayMaxTotalBytes bounds total materialized dirty draft bytes
	// across one searched root in one code-search leg.
	codeDraftOverlayMaxTotalBytes int64 = 8 << 20
)

// CodeDraft is an authorized caller's unsaved editor buffer for one path
// inside a searched project root.
type CodeDraft struct {
	Path    string
	Content string
}

// CodeDraftOverlay pairs materialized dirty buffers with the exact paths of
// dirty drafts omitted by row, per-document byte, or total materialized byte
// caps so the code leg excludes stale on-disk bytes and reports skipped files.
type CodeDraftOverlay struct {
	Drafts       []CodeDraft
	OmittedPaths []string
}

// CodeDraftProvider returns bounded active dirty editor drafts visible to ctx
// for one (projectID, rootID) pair. Implementations must derive caller and
// workspace authorization from ctx/request rather than trusting IDs alone.
type CodeDraftProvider func(ctx context.Context, projectID, rootID string, maxDocs int, maxDocBytes, maxTotalBytes int64) (CodeDraftOverlay, error)

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
