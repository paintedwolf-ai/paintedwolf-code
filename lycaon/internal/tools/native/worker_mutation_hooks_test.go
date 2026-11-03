package native

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

type workerCoordSpy struct {
	before     int
	after      int
	writeFacts []tools.WorkerFileObservationFacts
}

func (s *workerCoordSpy) BeforeWorkerWrite(context.Context, tools.ToolContext, string) error {
	s.before++
	return nil
}

func (s *workerCoordSpy) AfterWorkerWrite(context.Context, tools.ToolContext, string) {
	s.after++
}

func (s *workerCoordSpy) ObserveWorkerRead(context.Context, tools.ToolContext, tools.WorkerFileObservationFacts) {
}

func (s *workerCoordSpy) ObserveWorkerWrite(_ context.Context, _ tools.ToolContext, facts tools.WorkerFileObservationFacts) {
	s.writeFacts = append(s.writeFacts, facts)
}

func (s *workerCoordSpy) ReleaseReservations(context.Context, string, string) error {
	return nil
}

func (s *workerCoordSpy) EnsureBranch(_ context.Context, tctx tools.ToolContext) (tools.ToolContext, error) {
	return tctx, nil
}

func TestWorkerMutationHooksRunOnInvestigateSurface(t *testing.T) {
	spy := &workerCoordSpy{}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1"},
		Turn:     tools.InvocationTurn{TurnSurfaceID: toolcontract.SurfaceImplementInvestigate},
		Source:   tools.InvocationSource{WorkerCoord: spy},
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
		Turn:   tools.InvocationTurn{TurnSurfaceID: "implement_dispatch"},
		Source: tools.InvocationSource{WorkerCoord: spy},
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
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1"},
		Turn:     tools.InvocationTurn{TurnSurfaceID: "implement_dispatch"},
		Source:   tools.InvocationSource{WorkerCoord: spy},
	}
	if err := beforeWorkerMutation(context.Background(), tctx, "src/foo.go"); err != nil {
		t.Fatalf("beforeWorkerMutation: %v", err)
	}
	afterWorkerMutation(context.Background(), tctx, "src/foo.go")
	if spy.before != 1 || spy.after != 1 {
		t.Fatalf("worker hooks before=%d after=%d want 1/1", spy.before, spy.after)
	}
	observeCommittedWorkerWrite(context.Background(), tctx, "/tmp/src/foo.go", "sha-committed-1", 42)
	if len(spy.writeFacts) != 1 || spy.writeFacts[0].SHA256 != "sha-committed-1" || spy.writeFacts[0].Size != 42 || !spy.writeFacts[0].Complete || spy.writeFacts[0].Layer != "private" {
		t.Fatalf("unexpected writeFacts: %+v", spy.writeFacts)
	}
}
