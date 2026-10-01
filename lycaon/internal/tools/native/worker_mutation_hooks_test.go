package native

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

type workerCoordSpy struct {
	before int
	after  int
}

func (s *workerCoordSpy) BeforeWorkerWrite(context.Context, tools.ToolContext, string) error {
	s.before++
	return nil
}

func (s *workerCoordSpy) AfterWorkerWrite(context.Context, tools.ToolContext, string) {
	s.after++
}

func (s *workerCoordSpy) ReleaseWorkerReservations(context.Context, string, string) error {
	return nil
}

func (s *workerCoordSpy) EnsureWorkerBranch(_ context.Context, tctx tools.ToolContext) (tools.ToolContext, error) {
	return tctx, nil
}

func TestWorkerMutationHooksRunOnInvestigateSurface(t *testing.T) {
	spy := &workerCoordSpy{}
	tctx := tools.ToolContext{
		WorkerJobID:   "job-1",
		TurnSurfaceID: tools.SurfaceImplementInvestigate,
		WorkerCoord:   spy,
	}
	if err := beforeWorkerMutation(context.Background(), tctx, "src/foo.go"); err != nil {
		t.Fatalf("beforeWorkerMutation: %v", err)
	}
	afterWorkerMutation(context.Background(), tctx, "src/foo.go")
	if spy.before != 1 || spy.after != 1 {
		t.Fatalf("investigate worker hooks before=%d after=%d want 1/1", spy.before, spy.after)
	}
}

func TestSkipWorkerMutationHooksWithoutWorkerJobID(t *testing.T) {
	spy := &workerCoordSpy{}
	tctx := tools.ToolContext{
		TurnSurfaceID: "implement_dispatch",
		WorkerCoord:   spy,
	}
	if err := beforeWorkerMutation(context.Background(), tctx, "src/foo.go"); err != nil {
		t.Fatalf("beforeWorkerMutation: %v", err)
	}
	afterWorkerMutation(context.Background(), tctx, "src/foo.go")
	if spy.before != 0 || spy.after != 0 {
		t.Fatalf("coordinator direct write must skip worker hooks before=%d after=%d", spy.before, spy.after)
	}
}

func TestWorkerMutationHooksRunForWorkerJob(t *testing.T) {
	spy := &workerCoordSpy{}
	tctx := tools.ToolContext{
		WorkerJobID:   "job-1",
		TurnSurfaceID: "implement_dispatch",
		WorkerCoord:   spy,
	}
	if err := beforeWorkerMutation(context.Background(), tctx, "src/foo.go"); err != nil {
		t.Fatalf("beforeWorkerMutation: %v", err)
	}
	afterWorkerMutation(context.Background(), tctx, "src/foo.go")
	if spy.before != 1 || spy.after != 1 {
		t.Fatalf("worker hooks before=%d after=%d want 1/1", spy.before, spy.after)
	}
}
