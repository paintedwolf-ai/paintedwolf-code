package phases_test

import (
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

func toolContext(agent, sessionID, dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return tools.ToolContext{Agent: agent, SessionID: sessionID, Roots: roots, ActiveRootID: "r1"}
}
