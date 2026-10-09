package workerworkspace

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestBeforeWorkerWriteRecordsInvestigateSurface(t *testing.T) {
	mgr := New(nil, nil, nil)
	touches := NewTouchLedger()
	mgr.SetTouchLedger(touches)

	tctx := tools.ToolContext{
		WorkerJobID:      "job-1",
		TurnSurfaceID:    tools.SurfaceImplementInvestigate,
		HandoffSessionID: "parent",
		HandoffAgentID:   "job-1",
	}
	testutil.FailErr(t, "record worker write", mgr.BeforeWorkerWrite(context.Background(), tctx, "src/foo.go"))
	if got := touches.Paths("job-1"); len(got) != 1 || got[0] != "src/foo.go" {
		t.Fatalf("investigate worker touches = %v want [src/foo.go]", got)
	}
}

func TestBeforeWorkerWriteRecordsWorkerTouchOnOrchestrateSurface(t *testing.T) {
	mgr := New(nil, nil, nil)
	touches := NewTouchLedger()
	mgr.SetTouchLedger(touches)

	tctx := tools.ToolContext{
		WorkerJobID:      "job-1",
		TurnSurfaceID:    "implement_dispatch",
		HandoffSessionID: "parent",
		HandoffAgentID:   "job-1",
	}
	testutil.FailErr(t, "record worker write", mgr.BeforeWorkerWrite(context.Background(), tctx, "src/foo.go"))
	if got := touches.Paths("job-1"); len(got) != 1 || got[0] != "src/foo.go" {
		t.Fatalf("worker touch paths = %v want [src/foo.go]", got)
	}
}
