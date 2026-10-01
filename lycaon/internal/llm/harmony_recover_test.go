package llm

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func TestRecoverHarmonyCompletionPromotesCommentaryWhenToolsOffered(t *testing.T) {
	t.Parallel()
	c := &modelcall.Completion{Content: `analysisNeed curl.` +
		`assistantcommentary to=functions.command json{"command":"curl http://localhost:3000","cwd":"."}` +
		`assistantfinal{"leg_status":"complete","brief":"done"}`}
	out := RecoverHarmonyCompletion(c, true)
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "command" {
		t.Fatalf("tool calls = %+v", out.ToolCalls)
	}
	if strings.TrimSpace(out.Content) != "" {
		t.Fatalf("content should be empty so the tool loop can run, got %q", out.Content)
	}
	if !strings.Contains(out.Reasoning, "Need curl") {
		t.Fatalf("reasoning = %q", out.Reasoning)
	}
}

func TestRecoverHarmonyCompletionKeepsFinalWhenToolsOmitted(t *testing.T) {
	t.Parallel()
	c := &modelcall.Completion{Content: `assistantcommentary to=functions.command json{"command":"curl http://localhost:3000"}` +
		`assistantfinal{"leg_status":"complete","brief":"done"}`}
	out := RecoverHarmonyCompletion(c, false)
	if len(out.ToolCalls) != 0 {
		t.Fatalf("prose-only turn must not promote commentary: %+v", out.ToolCalls)
	}
	if !strings.Contains(out.Content, `"leg_status":"complete"`) {
		t.Fatalf("content = %q", out.Content)
	}
}

func TestRecoverHarmonyCompletionPeelsRepeatedRoleJSON(t *testing.T) {
	t.Parallel()
	c := &modelcall.Completion{Content: `assistantassistant{"leg_status":"complete","brief":"ok"}`}
	out := RecoverHarmonyCompletion(c, false)
	if out.Content != `{"leg_status":"complete","brief":"ok"}` {
		t.Fatalf("content = %q", out.Content)
	}
}

func TestRecoverHarmonyCompletionLeavesOrdinaryProse(t *testing.T) {
	t.Parallel()
	c := &modelcall.Completion{Content: "analysis of the heap shows a leak"}
	out := RecoverHarmonyCompletion(c, true)
	if out.Content != c.Content || len(out.ToolCalls) != 0 {
		t.Fatalf("ordinary prose rewritten: %+v", out)
	}
}
