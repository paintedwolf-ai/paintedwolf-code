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
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const transitionShellTemplate = "partials/coordinator-mode-transition-shell.md"

func TestRenderTransitionShellExplicitEntry(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	vars := map[string]any{"project_dir": t.TempDir()}
	if err := prompts.MergeCoordinatorSurfacePathVars("implement_synthesis", nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("path vars: %v", err)
	}
	err := prompts.MergeCoordinatorPromptVars(
		"implement_synthesis",
		prompts.ExecutionModePromptTransition{
			ExecutionMode:        surface.ExecutionModeFamilyWrapup,
			ExecutionModeEntered: surface.ExecutionModeFamilyWrapup,
		},
		prompts.CoordinatorPromptGates{},
		vars,
	)
	testutil.FailErr(t, "merge coordinator prompt vars", err)
	block, err := pe.Render(context.Background(), transitionShellTemplate, vars)
	if err != nil {
		t.Fatalf("render transition shell: %v", err)
	}
	if !strings.Contains(block, "## Entered wrapup") {
		t.Fatalf("transition block = %q", block)
	}
}

func TestEnteredInvestigateRequiresCurrentCatalogOnEntryAndReturn(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})

	render := func(transition prompts.ExecutionModePromptTransition) string {
		vars := map[string]any{"project_dir": t.TempDir()}
		if err := prompts.MergeCoordinatorSurfacePathVars("implement_investigate", nil, vars, prompts.SurfaceTurn{}); err != nil {
			t.Fatalf("path vars: %v", err)
		}
		err := prompts.MergeCoordinatorPromptVars("implement_investigate", transition, prompts.CoordinatorPromptGates{}, vars)
		testutil.FailErr(t, "merge coordinator prompt vars", err)
		block, err := pe.Render(context.Background(), transitionShellTemplate, vars)
		if err != nil {
			t.Fatalf("render transition shell: %v", err)
		}
		return block
	}

	const deferredLine = "`request_tools`"

	// Returning from coordination still requires deferred tools to be loaded.
	returned := render(prompts.ExecutionModePromptTransition{
		ExecutionMode:         surface.ExecutionModeFamilyInvestigate,
		ExecutionModePrevious: surface.ExecutionModeFamilyOrchestrate,
		ExecutionModeEntered:  surface.ExecutionModeFamilyInvestigate,
		ExecutionModeLeft:     surface.ExecutionModeFamilyOrchestrate,
	})
	if !strings.Contains(returned, "## Entered investigate") || !strings.Contains(returned, deferredLine) || strings.Contains(returned, "back in your schema") {
		t.Fatalf("return-from-orchestrate must use the current deferred catalog, got %q", returned)
	}

	// First entry uses the same capability boundary.
	firstEntry := render(prompts.ExecutionModePromptTransition{
		ExecutionMode:        surface.ExecutionModeFamilyInvestigate,
		ExecutionModeEntered: surface.ExecutionModeFamilyInvestigate,
	})
	if !strings.Contains(firstEntry, "## Entered investigate") {
		t.Fatalf("first entry should still render the investigate partial, got %q", firstEntry)
	}
	if !strings.Contains(firstEntry, deferredLine) || strings.Contains(firstEntry, "back in your schema") {
		t.Fatalf("first investigate entry must require the current deferred catalog, got %q", firstEntry)
	}
}

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
	block, ok := eng.prependTransitionInject(context.Background(), sess, inject.CoordinatorTurnFrame{}, []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}, turn)
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
