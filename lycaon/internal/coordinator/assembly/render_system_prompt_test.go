package assembly

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderSystemPromptUnknownAgentFallsBack(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Prompts: prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}),
	})
	sess := &api.Session{AgentType: "nonexistent-agent-xyz", WorkspacePath: t.TempDir()}
	pe, _, err := engine.projectPromptsSnapshot(context.Background(), sess)
	testutil.FailErr(t, "engine.projectPromptsSnapshot failed", err)
	out, err := engine.renderSystemPromptWithEngine(context.Background(), pe, sess,
		"agents/coordinator-core.md", map[string]any{"project_dir": t.TempDir()})
	testutil.FailErr(t, "engine.renderSystemPromptWithEngine failed", err)
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected fallback template render")
	}
}
