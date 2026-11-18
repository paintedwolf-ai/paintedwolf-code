package actioncard

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestCallsOnOnePageShareAFoldAndNameTheirStep(t *testing.T) {
	for tool, step := range map[string]string{"page_act": "act", "page_snapshot": "snapshot", "page_close": "close"} {
		d := ForResult(api.ToolResult{Tool: tool, ToolArgs: map[string]any{"id": " page-1 "}}, nil)
		if d.FoldKey != "page:page-1" {
			t.Fatalf("%s: fold %q", tool, d.FoldKey)
		}
		if steps, _ := fact(d.Facts, api.ActionFactSteps); len(steps.Sequence) != 1 || steps.Sequence[0] != step {
			t.Fatalf("%s: steps %+v", tool, steps)
		}
	}
	if d := ForResult(api.ToolResult{Tool: "page_act"}, nil); d.FoldKey != "" {
		t.Fatalf("a call naming no page folded: %q", d.FoldKey)
	}
}
