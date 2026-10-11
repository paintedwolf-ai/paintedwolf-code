package command

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// appendBoundaryNotes records refusal guidance without changing authority.
func appendBoundaryNotes(toolName, sessionID string, outcome RunOutcome, res *hostcmd.Result) {
	if res == nil || !outcome.Boundary.Applied {
		return
	}
	stamped := confine.StampRefusal(
		toolName,
		sessionID,
		outcome.Boundary,
		confine.RefusalContext{
			MediatedNetwork:        res.Network,
			RemotePackageExecution: res.RemotePackageExecution,
			FailedStages:           hostcmd.FailedStages(res.Stages, res.ExitCode),
			Refusals:               outcome.Refusals,
		},
	)
	res.BoundaryRefusal = string(stamped.Attribution)
	res.GuidanceCodes = append([]string(nil), stamped.GuidanceCodes...)
	res.Observation = stamped.Observation
}
