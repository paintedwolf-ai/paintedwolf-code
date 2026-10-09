package assembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func testPromptSurface(e *AssemblyEngine) *promptSurface {
	prompt, _ := newAssemblyDomains(e.deps(), &e.cache)
	return prompt
}
func testTurnContext(e *AssemblyEngine) *turnContextAssembler {
	_, turn := newAssemblyDomains(e.deps(), &e.cache)
	return turn
}

func TestCompletionKeepsOneWiringSnapshot(t *testing.T) {
	engine := &AssemblyEngine{}
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)})
	oldReads, newReads := 0, 0
	replacement := AssemblyDeps{Prompts: pe, AgentsMDIndex: func(context.Context, *api.Session) (api.Message, bool) {
		newReads++
		return api.Message{Content: "replacement wiring"}, true
	}}
	engine.SetDeps(AssemblyDeps{Prompts: pe,
		WorkspaceRoots: func(context.Context, *api.Session) ([]map[string]any, int, string) {
			engine.SetDeps(replacement)
			return nil, 0, ""
		},
		AgentsMDIndex: func(context.Context, *api.Session) (api.Message, bool) {
			oldReads++
			return api.Message{Content: "captured wiring"}, true
		},
	})
	session := &api.Session{ID: "snapshot", ParentSessionID: "parent", AgentType: "repo-researcher", WorkspacePath: t.TempDir()}
	engine.BeginPromptTurn(session.ID)
	if _, err := engine.BuildCompletionMessages(t.Context(), session, nil, nil); err != nil {
		testutil.FailErr(t, "build completion from captured wiring", err)
	}
	if oldReads != 1 || newReads != 0 {
		t.Fatalf("first completion read old=%d replacement=%d", oldReads, newReads)
	}
	if _, err := engine.BuildCompletionMessages(t.Context(), session, nil, nil); err != nil {
		testutil.FailErr(t, "build completion from captured wiring", err)
	}
	if oldReads != 1 || newReads != 1 {
		t.Fatalf("next completion read old=%d replacement=%d", oldReads, newReads)
	}
}
