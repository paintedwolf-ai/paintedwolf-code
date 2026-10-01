package assembly_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderPromptDumpInvestigateFixture(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	testutil.FailErr(t, "filepath.Abs failed", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	out, err := assembly.RenderPromptDump(context.Background(), engine, root, assembly.PromptDumpOptions{
		FixtureName: "implement_investigate_idle",
		SessionID:   "dump-session",
		UserPrompt:  "fix auth middleware",
	})
	if err != nil {
		t.Fatalf("RenderPromptDump: %v", err)
	}
	summaryGuidance, err := engine.Render(context.Background(), "units/native-summarize-tool.md", map[string]any{
		"profile_has_summarize": true,
	})
	testutil.FailErr(t, "render summarize guidance", err)
	if strings.TrimSpace(summaryGuidance) == "" {
		t.Fatal("summarize guidance is empty")
	}
	for _, want := range []string{
		"# Coordinator prompt dump",
		"surface_id: `implement_investigate`",
		"## core",
		"Workspace: " + root,
		"load through `request_tools`",
		"Host runner and sandbox",
		strings.TrimSpace(summaryGuidance),
		"excludes device/project skills",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in dump:\n%s", want, out)
		}
	}
	if strings.Contains(out, "No folder is attached") {
		t.Fatal("fixture workspace was rendered as folderless")
	}
}

func TestResolveTurnProfileForSurface(t *testing.T) {
	profile := surface.ResolveTurnProfileForSurface(
		"implement_investigate",
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
	)
	if profile.SurfaceID != "implement_investigate" {
		t.Fatalf("surface = %q", profile.SurfaceID)
	}
}

func TestHostTurnWaitOnly(t *testing.T) {
	if !loopwake.HostTurnWaitOnly([]string{"wait"}) {
		t.Fatal("expected wait-only turn")
	}
	if loopwake.HostTurnWaitOnly([]string{"task", "wait"}) {
		t.Fatal("multi-tool turn")
	}
}
