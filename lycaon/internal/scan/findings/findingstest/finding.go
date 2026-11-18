package findingstest

import (
	"github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

// Finding builds a SecurityFinding for tests.
func Finding(ruleID string, level api.FindingLevel, message, uri string, line int) api.SecurityFinding {
	return findings.BuildSecurityFinding(findings.FindingBuildOpts{
		DriverID: "fixture",
		RuleID:   ruleID,
		Level:    level,
		Message:  message,
		Kind:     api.FindingKindSAST,
		Locations: []api.SecurityFindingLocation{{
			URI:       uri,
			StartLine: line,
		}},
	})
}
