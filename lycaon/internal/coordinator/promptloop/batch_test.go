package promptloop

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolContextForCallPreservesCompiledSurface(t *testing.T) {
	loop := &PromptLoop{Deps: PromptLoopDeps{
		RefreshToolContext: func(context.Context, *api.Session, inject.Machine) (tools.ToolContext, error) {
			return tools.ToolContext{Agent: "coordinator"}, nil
		},
	}}
	base := tools.ToolContext{
		EditorReadBases:      tools.NewAgentReadBases(map[string]int64{"document": 7}),
		TurnSurfaceID:        tools.SurfaceImplementInvestigate,
		TurnToolPlan:         toolsurface.Compile([]string{"read"}, []string{"write", "verify"}),
		TurnOfferedToolNames: []string{"read"},
		TurnWritePinRootID:   "root-2",
		TurnWritePinGlobs:    []string{"src/**", "README.md"},
	}
	got, err := loop.toolContextForCall(t.Context(), &api.Session{}, base, inject.Machine{})
	testutil.FailErr(t, "toolContextForCall", err)
	if got.EditorReadBases != base.EditorReadBases || got.TurnSurfaceID != base.TurnSurfaceID ||
		!slices.Equal(got.TurnToolPlan.ImmediateNames(), base.TurnToolPlan.ImmediateNames()) ||
		!slices.Equal(got.TurnToolPlan.DeferredNames(), base.TurnToolPlan.DeferredNames()) ||
		!slices.Equal(got.TurnOfferedToolNames, base.TurnOfferedToolNames) ||
		got.TurnWritePinRootID != base.TurnWritePinRootID ||
		!slices.Equal(got.TurnWritePinGlobs, base.TurnWritePinGlobs) {
		t.Fatalf("refreshed surface = %+v, want compile from %+v", got, base)
	}
	got.TurnWritePinGlobs[0] = "mutated"
	got.TurnOfferedToolNames[0] = "mutated"
	if !base.TurnToolPlan.Immediate("read") || !base.TurnToolPlan.Deferred("write") || base.TurnWritePinGlobs[0] != "src/**" {
		t.Fatal("refreshed turn slices alias the base context")
	}
	if base.TurnOfferedToolNames[0] != "read" {
		t.Fatal("refreshed offers alias the base context")
	}
}

func TestReorderToolBatchExecutionUsesCatalogOrder(t *testing.T) {
	calls := []api.ToolCall{
		{Name: "summarize", Args: map[string]any{"path": "a.go"}},
		{Name: "read", Args: map[string]any{"path": "b.go"}},
		{Name: "summarize", Args: map[string]any{"path": "c.go"}},
	}
	got := reorderToolBatchExecution(batchTestRegistry(t, "summarize", "read"), calls)
	want := []string{"read", "summarize", "summarize"}
	if len(got) != len(want) {
		t.Fatalf("len = %d want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("got[%d] = %q want %q", i, got[i].Name, name)
		}
	}
}

func TestReorderToolBatchExecution_summarizeBeforeWait(t *testing.T) {
	calls := []api.ToolCall{
		{Name: "wait", Args: map[string]any{"timeout_ms": 60_000}},
		{Name: "summarize", Args: map[string]any{"path": "a.go"}},
		{Name: "read", Args: map[string]any{"path": "b.go"}},
	}
	got := reorderToolBatchExecution(batchTestRegistry(t, "wait", "summarize", "read"), calls)
	want := []string{"read", "summarize", "wait"}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("got[%d] = %q want %q (full=%v)", i, got[i].Name, name, toolCallNames(got))
		}
	}
}

func TestPartitionToolBatchRuns_summarizeIsolatedFromReads(t *testing.T) {
	calls := []api.ToolCall{
		{Name: "read", Args: map[string]any{"path": "a.go"}},
		{Name: "grep", Args: map[string]any{"pattern": "foo"}},
		{Name: "summarize", Args: map[string]any{"path": "pkg/"}},
		{Name: "summarize", Args: map[string]any{"path": "README.md"}},
	}
	runs := partitionToolBatchRuns(batchTestRegistry(t, "read", "grep", "summarize"), calls)
	if len(runs) != 2 {
		t.Fatalf("runs = %d want 2 (%v)", len(runs), runs)
	}
	if !slices.Equal(toolCallNames(runs[0]), []string{"read", "grep"}) {
		t.Fatalf("run0 = %v want [read grep]", toolCallNames(runs[0]))
	}
	if !slices.Equal(toolCallNames(runs[1]), []string{"summarize", "summarize"}) {
		t.Fatalf("run1 = %v want [summarize summarize]", toolCallNames(runs[1]))
	}
}

func TestConcurrentRunLimit_summarizeCapped(t *testing.T) {
	run := []api.ToolCall{
		{Name: "summarize"},
		{Name: "summarize"},
		{Name: "summarize"},
	}
	reg := batchTestRegistry(t, "summarize", "read", "grep")
	if got := concurrentRunLimit(reg, run); got != 4 {
		t.Fatalf("summarize concurrency = %d want 4", got)
	}
	if got := concurrentRunLimit(reg, []api.ToolCall{{Name: "read"}, {Name: "grep"}}); got != spawn.MaxConcurrentToolCalls {
		t.Fatalf("read/grep concurrency = %d want %d", got, spawn.MaxConcurrentToolCalls)
	}
}

func TestConcurrentRunLimitUsesTightestContract(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	handler := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	for name, limit := range map[string]int{"wide": 8, "narrow": 3, "single": 1} {
		testutil.FailErr(t, "register "+name, reg.RegisterDefinition(tools.Definition{
			Meta: tools.ToolMeta{Name: name},
			Contract: toolcontract.Contract{
				Owner: "test", Batch: toolcontract.BatchShared, BatchConcurrencyLimit: limit,
			},
			Handler: handler,
		}))
	}
	for _, run := range [][]api.ToolCall{
		{{Name: "wide"}, {Name: "narrow"}},
		{{Name: "narrow"}, {Name: "wide"}},
	} {
		if got := concurrentRunLimit(reg, run); got != 3 {
			t.Fatalf("batch concurrency = %d want 3 for %v", got, toolCallNames(run))
		}
	}
	if got := concurrentRunLimit(reg, []api.ToolCall{{Name: "wide"}, {Name: "single"}}); got != 1 {
		t.Fatalf("batch concurrency = %d want 1", got)
	}
}

func TestPartitionToolBatchRuns_webResearchRequiresExplicitOptIn(t *testing.T) {
	reg := batchTestRegistry(t, "web_search", "fetch_url")
	serial := []api.ToolCall{
		{Name: "web_search", Args: map[string]any{"query": "one"}},
		{Name: "web_search", Args: map[string]any{"query": "two"}},
	}
	if runs := partitionToolBatchRuns(reg, serial); len(runs) != 2 {
		t.Fatalf("serial runs = %v want two singleton runs", runs)
	}
	parallel := []api.ToolCall{
		{Name: "web_search", Args: map[string]any{"query": "one", "parallel": true}},
		{Name: "web_search", Args: map[string]any{"query": "two", "parallel": true}},
	}
	runs := partitionToolBatchRuns(reg, parallel)
	if len(runs) != 1 || len(runs[0]) != 2 {
		t.Fatalf("parallel runs = %v want one two-call run", runs)
	}
	if got := concurrentRunLimit(reg, runs[0]); got != 4 {
		t.Fatalf("web_search concurrency = %d want 4", got)
	}
	writes := []api.ToolCall{
		{Name: "fetch_url", Args: map[string]any{"url": "https://example.test/a", "dest": "a.bin", "parallel": true}},
		{Name: "fetch_url", Args: map[string]any{"url": "https://example.test/b", "dest": "b.bin", "parallel": true}},
	}
	if runs := partitionToolBatchRuns(reg, writes); len(runs) != 2 {
		t.Fatalf("destination-writing fetch runs = %v want two singleton runs", runs)
	}
}

func batchTestRegistry(t *testing.T, names ...string) *tools.DefaultRegistry {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	for _, name := range names {
		testutil.FailErr(t, "register "+name, reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "", nil
		}))
	}
	return reg
}

func toolCallNames(calls []api.ToolCall) []string {
	out := make([]string, len(calls))
	for i := range calls {
		out[i] = calls[i].Name
	}
	return out
}
