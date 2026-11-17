package wiring

import (
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func AnyMessageContains(msgs []wire.Message, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, sub) || (m.WorkerSummary != nil && strings.Contains(m.WorkerSummary.Envelope, sub)) {
			return true
		}
	}
	return false
}

func WiringToolContext(sessionID, dir string, workerJobID ...string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID},
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
	}
	if len(workerJobID) > 0 {
		tctx.Identity.WorkerJobID = workerJobID[0]
		tctx.Identity.ParentSessionID = "parent"
	}
	return tctx
}

func SchemaHasTool(metas []tools.ToolMeta, name string) bool {
	for _, meta := range metas {
		if meta.Name == name {
			return true
		}
	}
	return false
}
