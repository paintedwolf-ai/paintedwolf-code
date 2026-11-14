package projectsource

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/formatconfig"
)

// ResolveSourceFormatSettings resolves the formatting settings each tool's
// configuration puts in effect for a root-addressed path. The path need not
// exist; it must stay inside the root.
func ResolveSourceFormatSettings(p ProjectSource, pathQuery, rootID string) (string, []formatconfig.Source, error) {
	if p == nil || len(p.SourceRoots()) == 0 {
		return "", nil, ErrSourceNoRoot
	}
	pathQuery = strings.TrimSpace(pathQuery)
	if pathQuery == "" || strings.TrimSpace(rootID) == "" {
		return "", nil, ErrSourcePathInvalid
	}
	roots, err := scopedRootsForSource(p, rootID)
	if err != nil {
		return "", nil, err
	}
	root := roots[0]
	_, rel, ok := evidence.ResolveCitationAbs(root.Path, pathQuery)
	if !ok {
		return "", nil, ErrSourcePathDenied
	}
	sources, err := formatconfig.Resolve(root.Path, rel)
	if err != nil {
		return "", nil, err
	}
	return rel, sources, nil
}
