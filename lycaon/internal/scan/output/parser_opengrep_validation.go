package output

import (
	"fmt"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

func validateOpengrepFindingShape(finding api.SecurityFinding) error {
	if strings.TrimSpace(strings.TrimPrefix(finding.RuleID, "opengrep:")) == "" {
		return fmt.Errorf("opengrep_json: finding has no rule identity")
	}
	var invalid error
	scanfindings.VisitFindingLocations(&finding, func(location *api.SecurityFindingLocation) {
		if invalid != nil {
			return
		}
		if strings.TrimSpace(location.URI) == "" {
			invalid = fmt.Errorf("opengrep_json: finding has no source path")
			return
		}
		if location.StartLine < 1 || location.StartColumn < 1 || location.EndLine < 1 || location.EndColumn < 1 ||
			location.EndLine < location.StartLine ||
			(location.EndLine == location.StartLine && location.EndColumn < location.StartColumn) {
			invalid = fmt.Errorf("opengrep_json: invalid evidence span: %s:%d:%d-%d:%d", location.URI,
				location.StartLine, location.StartColumn, location.EndLine, location.EndColumn)
		}
	})
	return invalid
}
