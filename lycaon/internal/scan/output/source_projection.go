package output

import (
	"fmt"
	"path/filepath"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

// RemapOpengrepSources maps all primary and nested evidence spans before source validation.
func RemapOpengrepSources(result *Result, origins map[string]sourceview.Origin, project string) error {
	originFor := func(path string) (sourceview.Origin, bool) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(project, path)
		}
		origin, ok := origins[path]
		return origin, ok
	}
	for i := range result.Findings {
		var locationErr error
		scanfindings.VisitFindingLocations(&result.Findings[i], func(loc *api.SecurityFindingLocation) {
			origin, exists := originFor(loc.URI)
			if !exists {
				return
			}
			start, end, ok := origin.Map.MapSpan(sourceview.Position{Line: loc.StartLine, Column: loc.StartColumn}, sourceview.Position{Line: loc.EndLine, Column: loc.EndColumn})
			if !ok {
				locationErr = fmt.Errorf("projected evidence has no original source span: %s:%d:%d", loc.URI, loc.StartLine, loc.StartColumn)
				return
			}
			loc.URI = origin.Path
			loc.StartLine, loc.StartColumn = start.Line, start.Column
			loc.EndLine, loc.EndColumn = end.Line, end.Column
		})
		if locationErr != nil {
			return locationErr
		}
		scanfindings.RefreshFingerprint(&result.Findings[i])
	}
	for i := range result.Warnings {
		warning := &result.Warnings[i]
		origin, exists := originFor(warning.File)
		if !exists {
			continue
		}
		warning.Message = strings.ReplaceAll(warning.Message, warning.File, origin.Path)
		warning.File = origin.Path
		if warning.Kind == api.ScanWarningFilePartialParse {
			warning.Message = "Embedded script parsing is incomplete; results may be incomplete."
		}
		if warning.StartLine > 0 {
			point, ok := origin.Map.MapPosition(sourceview.Position{Line: warning.StartLine, Column: warning.StartColumn})
			if ok {
				warning.StartLine, warning.StartColumn = point.Line, point.Column
			} else {
				warning.StartLine, warning.StartColumn = 0, 0
			}
		}
	}
	return nil
}
