package workerworkspace

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestBeforeWorkerWriteRecordsInvestigateSurface(t *testing.T) {
	mgr := New(nil, nil, nil)
	touches := mgr.Touches

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1",
			HandoffSessionID: "parent",
			HandoffAgentID:   "job-1"},
		Turn: tools.InvocationTurn{TurnSurfaceID: toolcontract.SurfaceImplementInvestigate},
	}
	testutil.FailErr(t, "record worker write", mgr.BeforeWorkerWrite(context.Background(), tctx, "src/foo.go"))
	if got := touches.Paths("job-1"); len(got) != 1 || got[0] != "src/foo.go" {
		t.Fatalf("investigate worker touches = %v want [src/foo.go]", got)
	}
}

func TestBeforeWorkerWriteRecordsWorkerTouchOnOrchestrateSurface(t *testing.T) {
	mgr := New(nil, nil, nil)
	touches := mgr.Touches

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1",
			HandoffSessionID: "parent",
			HandoffAgentID:   "job-1"},
		Turn: tools.InvocationTurn{TurnSurfaceID: "implement_dispatch"},
	}
	testutil.FailErr(t, "record worker write", mgr.BeforeWorkerWrite(context.Background(), tctx, "src/foo.go"))
	if got := touches.Paths("job-1"); len(got) != 1 || got[0] != "src/foo.go" {
		t.Fatalf("worker touch paths = %v want [src/foo.go]", got)
	}
}
