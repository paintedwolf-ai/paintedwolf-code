package assembly

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResearchGuidanceFollowsActualOfferedTools(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		loaded                 map[string]bool
		enabled, search, fetch bool
	}{
		{"deferred", nil, true, false, false},
		{"search only", map[string]bool{"web_search": true}, true, true, false},
		{"fetch only", map[string]bool{"fetch_url": true}, true, false, true},
		{"research family", map[string]bool{"fetch_url": true, "web_search": true}, true, true, true},
		{"disabled", map[string]bool{"fetch_url": true, "web_search": true}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)})
			engine := &AssemblyEngine{}
			engine.SetDeps(AssemblyDeps{Prompts: pe, WebSearchEnabled: func() bool { return tc.enabled }, LoadedTools: func(string) map[string]bool { return tc.loaded }})
			sess := &api.Session{ID: "research", AgentType: "coordinator", Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
			frame := inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{PhaseCoordinatorSurface: "implement_investigate"}}
			out, _, _, err := testPromptSurface(engine).renderTripartiteCoordinatorPrompt(t.Context(), pe, sess, frame, nil, "", nil, map[string]any{"root_count": 1, "project_dir": sess.WorkspacePath})
			testutil.FailErr(t, "render research surface", err)
			if strings.Contains(out, "`web_search` discovers sources.") != tc.search {
				t.Fatalf("search guidance disagrees with offered tools: %s", out)
			}
			if strings.Contains(out, "Read relevant `offset`/`limit` sections;") != tc.fetch {
				t.Fatalf("fetch guidance disagrees with offered tools: %s", out)
			}
			if tc.fetch && !strings.Contains(out, "[title](observed URL)") {
				t.Fatal("research citation syntax missing")
			}
		})
	}
}
