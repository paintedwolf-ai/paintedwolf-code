package output

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scansourceview "github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

// ValidateOpengrepLocations binds every reported evidence span to snapshot bytes.
func ValidateOpengrepLocations(result *Result, project string) error {
	lines := make(map[string][]int)
	var invalid error
	for i := range result.Findings {
		scanfindings.VisitFindingLocations(&result.Findings[i], func(location *api.SecurityFindingLocation) {
			if invalid == nil {
				invalid = validateOpengrepLocation(*location, project, lines)
			}
		})
	}
	return invalid
}

func validateOpengrepLocation(location api.SecurityFindingLocation, project string, cache map[string][]int) error {
	path := location.URI
	if !filepath.IsAbs(path) {
		path = filepath.Join(project, path)
	}
	if err := scansourceview.AssertScanPathWithinProject(project, path); err != nil {
		return fmt.Errorf("opengrep evidence path: %w", err)
	}
	lengths, exists := cache[path]
	if !exists {
		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("opengrep evidence source: %w", err)
		}
		for line := range bytes.SplitSeq(source, []byte{'\n'}) {
			lengths = append(lengths, len(line))
		}
		cache[path] = lengths
	}
	valid := func(line, column int) bool {
		return line > 0 && line <= len(lengths) && column > 0 && column <= lengths[line-1]+1
	}
	if !valid(location.StartLine, location.StartColumn) || !valid(location.EndLine, location.EndColumn) ||
		location.EndLine < location.StartLine || (location.EndLine == location.StartLine && location.EndColumn < location.StartColumn) {
		return fmt.Errorf("opengrep evidence span outside source: %s:%d:%d-%d:%d", location.URI, location.StartLine, location.StartColumn, location.EndLine, location.EndColumn)
	}
	return nil
}
