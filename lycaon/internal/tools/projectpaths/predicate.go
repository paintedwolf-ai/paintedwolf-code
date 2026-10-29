package projectpaths

import (
	"path/filepath"
	"strings"
)

// TestPath probes a previously resolved operand through the same held root used
// by content reads. It never routes an access query through mutation admission.
func (s *ReadSession) TestPath(resolved Resolved, operator, sourcePath string) (bool, error) {
	loc := resolved.EffectLocation()
	root, err := s.heldRoot(loc.Root)
	if err != nil {
		return false, err
	}
	if strings.HasSuffix(sourcePath, "/") || strings.HasSuffix(sourcePath, string(filepath.Separator)) {
		loc.Rel += "/"
	}
	return root.TestPath(loc.Rel, operator)
}
