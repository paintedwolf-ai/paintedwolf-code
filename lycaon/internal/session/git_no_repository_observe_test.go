package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
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
	offered, withheld := render(true), render(false)
	if offered == output || withheld == output {
		t.Fatalf("the warning did not annotate the result: %q", offered)
	}
	if offered == withheld {
		t.Fatalf("both profiles were told the same: %q", offered)
	}
	if plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "command", map[string]any{"command": "git status"}, output, 1, guidance.ToolResultFacts{}); plain != output {
		t.Fatalf("a call with no miss was annotated: %q", plain)
	}
}
