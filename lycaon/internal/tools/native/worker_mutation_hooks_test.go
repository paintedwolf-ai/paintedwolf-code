package native

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

type workerCoordSpy struct {
	before int
	path   string
	after  int
}

func (s *workerCoordSpy) BeforeWorkerWrite(_ context.Context, _ tools.ToolContext, path string) error {
	s.before++
	s.path = path
	return nil
}

func (s *workerCoordSpy) AfterWorkerWrite(context.Context, tools.ToolContext, string) {
	s.after++
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
}

func TestWorkerMutationHookMapsNativeAbsolutePaths(t *testing.T) {
	branch := t.TempDir()
	spy := &workerCoordSpy{}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1"},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "primary", Path: branch, IsPrimary: true}},
			ActiveRootID: "primary", WorkerBranchRoot: branch,
			BranchWorkspace: testutil.CompleteBranchWorkspace{}, WorkerCoord: spy,
		},
	}
	testutil.FailErr(t, "reserve native worker path", beforeWorkerMutation(t.Context(), tc, filepath.Join(branch, "main.go")))
	if spy.before != 1 || spy.path != "main.go" {
		t.Fatalf("reservation = %d %q", spy.before, spy.path)
	}
	if err := beforeWorkerMutation(t.Context(), tc, filepath.Join(filepath.Dir(branch), "outside.go")); err == nil {
		t.Fatal("outside branch path admitted to reservation")
	}
	if spy.before != 1 {
		t.Fatal("outside branch path reached reservation")
	}
}
