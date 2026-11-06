package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestACOP01WriteLegGitWarningAnnotatesTheCommand(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-write-leg-git"}
	const output = `{"status":128}`
	render := func(offered bool) string {
		raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.GitNoRepositoryOnWriteLegCode, map[string]any{"source_history_offered": offered}, nil)
		out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "command", map[string]any{"command": "git status"}, output, 1, raised)
		if !facts.HasCode(toolrejection.GitNoRepositoryOnWriteLegCode) || !facts.Succeeded() {
			t.Fatalf("facts = %+v, want the warning on a result that still succeeded", facts)
		}
		return out
	}
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	seen := map[bool]bool{}
	for _, scenario := range hints.HintCodes[toolrejection.GitNoRepositoryOnWriteLegCode].Scenarios {
		offered, ok := scenario.Vars["source_history_offered"].(bool)
		if !ok {
			t.Fatalf("scenario %q states no source_history_offered", scenario.ID)
		}
		seen[offered] = true
		rendered := render(offered)
		for _, want := range scenario.ExpectContains {
			if !strings.Contains(rendered, want) {
				t.Fatalf("scenario %q: %q missing from the warning:\n%s", scenario.ID, want, rendered)
			}
		}
	}
	if !seen[true] || !seen[false] {
		t.Fatalf("scenarios cover %v, want both answers", seen)
	}
	if plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "command", map[string]any{"command": "git status"}, output, 1, guidance.ToolResultFacts{}); plain != output {
		t.Fatalf("a call with no miss was annotated: %q", plain)
	}
}
