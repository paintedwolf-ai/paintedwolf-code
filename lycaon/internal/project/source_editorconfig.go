package project

import (
	"strings"

	"github.com/lycaon/lycaon/internal/editorconfig"
	"github.com/lycaon/lycaon/internal/evidence"
)

// ResolveSourceEditorConfig resolves the EditorConfig pairs in effect for a
// root-addressed path. The path need not exist; it must stay inside the root.
func ResolveSourceEditorConfig(p *Project, pathQuery, rootID string) (string, editorconfig.Properties, error) {
	if p == nil || len(p.Roots) == 0 {
		return "", editorconfig.Properties{}, ErrSourceNoRoot
	}
	pathQuery = strings.TrimSpace(pathQuery)
	if pathQuery == "" || strings.TrimSpace(rootID) == "" {
		return "", editorconfig.Properties{}, ErrSourcePathInvalid
	}
	roots, err := scopedRootsForSource(p, rootID)
	if err != nil {
		return "", editorconfig.Properties{}, err
	}
	root := roots[0]
	_, rel, ok := evidence.ResolveCitationAbs(root.Path, pathQuery)
	if !ok {
		return "", editorconfig.Properties{}, ErrSourcePathDenied
	}
	props, err := editorconfig.Load(root.Path, rel)
	if err != nil {
		return "", editorconfig.Properties{}, err
	}
	return rel, props, nil
}
