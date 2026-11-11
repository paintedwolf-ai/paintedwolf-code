package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestACOP01WriteLegGitWarningAnnotatesTheCommand(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-write-leg-git"}
	const output = `{"status":128}`
	for _, offered := range []bool{true, false} {
		names := []string{"command"}
		if offered {
			names = append(names, "source_history")
		}
		ctx := tools.WithCallReach(context.Background(), toolreach.Offered(names))
		raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.GitNoRepositoryOnWriteLegCode, nil, nil)
		out, facts := mgr.ToolPolicy.AfterTool(ctx, sess, "command", map[string]any{"command": "git status"}, output, 1, raised)
		if !facts.HasCode(toolrejection.GitNoRepositoryOnWriteLegCode) || !facts.Succeeded() {
			t.Fatalf("facts = %+v, want the warning on a result that still succeeded", facts)
		}
		if strings.Contains(out, "`source_history(") != offered {
			t.Fatalf("offered=%v, warning:\n%s", offered, out)
		}
	}
	if plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "command", map[string]any{"command": "git status"}, output, 1, guidance.ToolResultFacts{}); plain != output {
		t.Fatalf("a call with no miss was annotated: %q", plain)
	}
}
