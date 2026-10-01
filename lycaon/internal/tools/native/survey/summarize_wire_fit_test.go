package survey

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// writeWireFitFixture fills the pack before envelope accounting.
func writeWireFitFixture(t *testing.T, dir string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("package dispatch\n\n")
	for s := 0; s < 60; s++ {
		fmt.Fprintf(&b, "// HandleRequestDispatchVariant documents pipeline case %d in detail.\n", s)
		fmt.Fprintf(&b, "func HandleRequestDispatchPipelineVariantCase%d() {\n", s)
		for l := 0; l < 8; l++ {
			fmt.Fprintf(&b, "\tprocessRequestDispatchPipelineStage(%d, %d) // stage padding line\n", s, l)
		}
		b.WriteString("}\n\n")
	}
	writeFile(t, dir, "pkg/request_dispatch_pipeline.go", b.String())
}

func TestSummarizeWireFitLandsUnderWireBudget(t *testing.T) {
	dir := t.TempDir()
	writeWireFitFixture(t, dir)
	ctx := context.Background()
	tctx := nativefixture.Context(dir)
	args := map[string]any{"path": "pkg/request_dispatch_pipeline.go", "task": "explain the dispatch pipeline"}

	caps := summarize.DefaultCaps()

	offCaps := caps
	offCaps.Pack.WireBudgetTokens = 0
	offTool := &SummarizeTool{Boundary: nativefixture.Boundary(t), Caps: offCaps}
	rawOff, err := offTool.Run(ctx, args, tctx)
	testutil.FailErr(t, "run without wire fit", err)
	if got := caps.EstimateTokens(rawOff); got <= caps.Pack.WireBudgetTokens {
		t.Fatalf("fixture too small: unfitted wire estimate %d ≤ wire budget %d", got, caps.Pack.WireBudgetTokens)
	}

	var observed summarize.Result
	tool := &SummarizeTool{Boundary: nativefixture.Boundary(t), Caps: caps, Observe: func(result summarize.Result) { observed = result }}
	raw, err := tool.Run(ctx, args, tctx)
	testutil.FailErr(t, "run with wire fit", err)
	if got := caps.EstimateTokens(raw); got > caps.Pack.WireBudgetTokens {
		t.Fatalf("wire estimate %d exceeds wire_budget_tokens %d — payload would spill at commit", got, caps.Pack.WireBudgetTokens)
	}
	resp := decodeSummarizeResponse(t, raw)
	if observed.Orchestration.Curator.BudgetTokensSpent == 0 {
		t.Fatalf("curator = %+v, want positive budget spent on the fitted briefing", observed.Orchestration.Curator)
	}
	if len(resp.Pack.Identity) == 0 || len(resp.Pack.Skeleton) == 0 {
		t.Fatal("fitted pack must keep identity and skeleton coverage")
	}
	if len(resp.Pack.Substance) == 0 {
		t.Fatal("fitted briefing discarded all implementing source")
	}
}

func TestSummarizeDirectoryWireFitRetainsImplementingSource(t *testing.T) {
	dir := t.TempDir()
	for i := range 24 {
		var body strings.Builder
		body.WriteString("package service\n\n")
		for j := range 20 {
			fmt.Fprintf(&body, "func Route%d() int { return %d }\n", j, j)
		}
		writeFile(t, dir, fmt.Sprintf("pkg/service%02d.go", i), body.String())
	}
	caps := summarize.DefaultCaps()
	tool := &SummarizeTool{Boundary: nativefixture.Boundary(t), Caps: caps}
	raw, err := tool.Run(context.Background(), map[string]any{"path": "pkg", "task": "explain service routing"}, nativefixture.Context(dir))
	testutil.FailErr(t, "run directory briefing through wire fit", err)
	resp := decodeSummarizeResponse(t, raw)
	if len(resp.Pack.Substance) == 0 || len(resp.Anchors) == 0 {
		t.Fatal("directory briefing must retain source windows and citable anchors")
	}
	if got := caps.EstimateTokens(raw); got > caps.Pack.WireBudgetTokens {
		t.Fatalf("directory briefing grew past wire cap: got=%d cap=%d", got, caps.Pack.WireBudgetTokens)
	}
}
