package assembly

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPrependTransitionInjectExplicitCause(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{WorkersInFlight: 1, PendingOverlayIDs: []string{}}
		},
		LoadExecutionModeState: func(context.Context, string) surface.ExecutionModeState { return surface.ExecutionModeState{} },
	}
	eng := &AssemblyEngine{}
	eng.SetDeps(deps)
	turn := &TurnAssemblyScratch{PromptTurnSeq: 1}
	turn.ModeTransitionCauses = []surface.ModeTransitionCause{{Kind: surface.ModeTransitionCauseWorkflowDefault, Mode: surface.ExecutionModeFamilyOrchestrate}}
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
	block, ok := testTurnContext(eng).prependTransitionInject(context.Background(), sess, inject.CoordinatorTurnFrame{}, []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}, turn)
	if !ok {
		t.Fatal("prependTransitionInject returned false")
	}
	if !strings.Contains(block, "## Entered orchestrate") {
		t.Fatalf("block = %q", block)
	}
}

func kickTestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
