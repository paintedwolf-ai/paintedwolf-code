package search

import (
	"context"
	"path"
	"strings"
)

// CodeDraft is an authorized caller's unsaved editor buffer for one path
// inside a searched project root.
type CodeDraft struct {
	Path    string
	Content string
}

// CodeDraftProvider returns active dirty editor drafts visible to ctx for one
// (projectID, rootID) pair. Implementations must derive caller and workspace
// authorization from ctx rather than trusting project or root IDs alone.
type CodeDraftProvider func(ctx context.Context, projectID, rootID string) ([]CodeDraft, error)

func normalizeDraftRelPath(raw string) (string, bool) {
	rel := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", false
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}
