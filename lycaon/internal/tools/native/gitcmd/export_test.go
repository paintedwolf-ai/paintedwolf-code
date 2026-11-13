package gitcmd

import (
	"context"
	"github.com/lycaon/lycaon/internal/confine"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools"
)

// UnconfinedLaunch runs fixture Git as the host, for tests of Git semantics
// rather than the launch boundary.
func UnconfinedLaunch(context.Context, tools.ToolContext, string, string, []confine.HostService) (exec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
	return exec.HostLaunch("git"), func(context.Context, tools.AgentLaunchOutcome) {}, nil
}
