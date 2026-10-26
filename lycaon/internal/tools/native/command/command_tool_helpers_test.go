package command_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func commandToolContext(root, sessionID, workerJobID string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID,
			WorkerJobID: workerJobID,
			Agent:       "implement"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}},
	}
}

// newCommandTool builds the retained confined runner used by one-shot
// captures and verify's background launches.
func newCommandTool(t *testing.T) (*command.CommandTool, *bgprocess.Registry) {
	t.Helper()
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"command": true}},
	})
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	return &command.CommandTool{
		Runner:     hostcmd.NewRunner(),
		Boundary:   boundary,
		Background: reg,
	}, reg
}
