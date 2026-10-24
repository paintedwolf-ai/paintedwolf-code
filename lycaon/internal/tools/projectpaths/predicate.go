package projectpaths

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// TestPath probes a previously resolved operand through the same held root used
// by content reads. It never routes an access query through mutation admission.
func (s *ReadSession) TestPath(resolved Resolved, operator, sourcePath string) (bool, error) {
	if settingsoverlay.IsRecoveryRelPath(resolved.ScopeRel) || settingsoverlay.HasRecoverySegment(resolved.Abs) || settingsoverlay.IsRecoveryOnlyDir(resolved.Abs) {
		return false, nil
	}
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
