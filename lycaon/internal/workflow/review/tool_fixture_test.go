package review_test

import (
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

func toolContext(agent, sessionID, dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return tools.ToolContext{Identity: tools.InvocationIdentity{Agent: agent, SessionID: sessionID},
		Source: tools.InvocationSource{Roots: roots, ActiveRootID: "r1"}}
}
