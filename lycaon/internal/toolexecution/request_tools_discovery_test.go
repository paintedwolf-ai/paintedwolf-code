package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestRequestDiscoveryOutageToExactActivation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status string
	}{
		{"unavailable", decide.ErrUnavailable, "ranking_unavailable"},
		{"timeout", decide.ErrDeadline, "ranking_unavailable"},
		{"fault", decide.ErrEngine, "ranking_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			activation := tools.NewMemoryActivation()
			d := &decidetest.Fake{Err: tc.err}
			var names []string
			for i := 0; i < 43; i++ {
				name := fmt.Sprintf("mcp_fixture_tool_%02d", i)
				names = append(names, name)
				testutil.FailErr(t, "register fixture", reg.RegisterDefinition(tools.Definition{
					Meta:     tools.ToolMeta{Name: name, Description: fmt.Sprintf("Operation %d", i), Source: tools.ToolSourceMCP, SourceID: "fixture", ArgsSchema: map[string]any{"type": "object"}},
					Contract: toolcontract.External("mcp:fixture"), Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil },
				}))
			}
			var receipt turnload.RequestToolsResult
			testutil.FailErr(t, "register discovery", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{
				Activation: activation, Boundary: fakeRequestBoundary{},
				Resolve: func(ctx context.Context, _ tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
					return turnload.ResolveRequest(ctx, d, turnload.RequestSpec{DeadlineMS: 1000, LoadAt: 2, MaxLoads: 1}, need, cards)
				},
				Record: func(_ context.Context, _ tools.ToolContext, _ turnload.RequestOutcome, result turnload.RequestToolsResult, _ time.Duration) {
					receipt = result
				},
			}))
			tctx := tools.ToolContext{SessionID: "s", TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, names[:42])}
			args := map[string]any{"need": "inspect the operation"}
			count := 0
			for {
				raw, err := reg.Run(t.Context(), "request_tools", args, tctx)
				testutil.FailErr(t, "discover", err)
				var result turnload.RequestToolsResult
				testutil.FailErr(t, "decode discovery", json.Unmarshal([]byte(raw), &result))
				if result.Discovery == nil || len(activation.Active("s")) != 0 || receipt.Discovery == nil || result.Note != "" {
					t.Fatalf("catalog loaded tools or lost receipt: %s", raw)
				}
				if count == 0 && result.Discovery.Status != tc.status {
					t.Fatalf("status = %s", result.Discovery.Status)
				}
				for _, entry := range result.Discovery.Entries {
					if entry.Name != names[count] || entry.Description == "" {
						t.Fatalf("entry = %+v", entry)
					}
					count++
				}
				if result.Discovery.NextNeed == "" {
					break
				}
				args["need"] = result.Discovery.NextNeed
			}
			if count != 42 || len(d.Ranks) != 1 {
				t.Fatalf("count=%d rank calls=%d", count, len(d.Ranks))
			}
			_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": names[41]}, tctx)
			testutil.FailErr(t, "exact retry", err)
			if !activation.Active("s")[names[41]] || len(activation.Active("s")) != 1 || len(d.Ranks) != 1 {
				t.Fatal("retry did not exclusively activate selection without ranking")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := reg.Run(ctx, "request_tools", map[string]any{"need": names[0]}, tctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled call = %v", err)
			}
			if activation.Active("s")[names[0]] {
				t.Fatal("cancellation activated a tool")
			}
		})
	}
}

func TestRequestDiscoveryPartialSelectionAndCancellation(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	registerRequestFixtureTools(t, reg, "read", "write", "edit")
	activation := tools.NewMemoryActivation()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resolve := func(_ context.Context, _ tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
		if need == "cancel during ranking" {
			cancel()
		}
		return turnload.ResolveRequest(t.Context(), nil, turnload.RequestSpec{}, need, cards)
	}
	testutil.FailErr(t, "register discovery", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{Activation: activation, Boundary: fakeRequestBoundary{}, Resolve: resolve}))
	tctx := tools.ToolContext{SessionID: "partial", TurnToolPlan: toolsurface.Compile([]string{"read", "request_tools"}, []string{"write", "edit"})}
	raw, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "write and inspect more"}, tctx)
	testutil.FailErr(t, "partial selection", err)
	var result turnload.RequestToolsResult
	testutil.FailErr(t, "decode partial result", json.Unmarshal([]byte(raw), &result))
	// The exact name loads with its declared companion; the rest goes to discovery.
	if !activation.Active("partial")["write"] || !activation.Active("partial")["edit"] || result.Discovery == nil || result.Discovery.Failure != turnload.RankingUnavailable {
		t.Fatalf("partial=%s", raw)
	}
	_, err = reg.Run(t.Context(), "request_tools", map[string]any{"need": "read"}, tctx)
	testutil.FailErr(t, "already loaded exact name", err)
	before := len(activation.Active("partial"))
	if _, err := reg.Run(ctx, "request_tools", map[string]any{"need": "cancel during ranking"}, tctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if len(activation.Active("partial")) != before {
		t.Fatal("canceled resolution activated tool")
	}
}

func TestRequestDiscoveryCursorSurvivesPartialActivation(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	activation := tools.NewMemoryActivation()
	var names []string
	for i := 0; i < 44; i++ {
		names = append(names, fmt.Sprintf("mcp_fixture_tool_%02d", i))
	}
	registerRequestFixtureTools(t, reg, names...)
	testutil.FailErr(t, "register discovery", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{Activation: activation, Boundary: fakeRequestBoundary{}}))
	tctx := tools.ToolContext{SessionID: "partial-page", TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, names)}
	need := names[0] + " and another operation"
	raw, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": need}, tctx)
	testutil.FailErr(t, "partial discovery", err)
	var result turnload.RequestToolsResult
	testutil.FailErr(t, "decode page", json.Unmarshal([]byte(raw), &result))
	if result.Discovery == nil || result.Discovery.Total != 43 || result.Discovery.Entries[0].Name != names[1] || result.Discovery.NextNeed == "" {
		t.Fatalf("partial page=%s", raw)
	}
	tctx.TurnToolPlan = toolsurface.Compile([]string{"request_tools", names[0]}, names[1:])
	next, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": result.Discovery.NextNeed}, tctx)
	testutil.FailErr(t, "continue after activation", err)
	testutil.FailErr(t, "decode continuation", json.Unmarshal([]byte(next), &result))
	if result.Discovery.Entries[0].Name != names[21] || len(activation.Active(tctx.SessionID)) != 1 {
		t.Fatalf("continuation=%s", next)
	}
}

func TestRequestHealthyRankingHasNoDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		score    float64
		nearest  int
		selected bool
	}{
		{"selected", 4, 0, true}, {"nearest", 0, 1, true}, {"no match", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			registerRequestFixtureTools(t, reg, "read")
			activation := tools.NewMemoryActivation()
			d := &decidetest.Fake{Scores: []float64{tc.score}}
			testutil.FailErr(t, "register request", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{
				Activation: activation, Boundary: fakeRequestBoundary{},
				Resolve: func(ctx context.Context, _ tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
					return turnload.ResolveRequest(ctx, d, turnload.RequestSpec{DeadlineMS: 1000, LoadAt: 2, MaxLoads: 1, NearestLoads: tc.nearest}, need, cards)
				},
			}))
			raw, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "inspect content"}, tools.ToolContext{SessionID: "healthy", TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"read"})})
			if tc.selected {
				testutil.FailErr(t, "healthy selection", err)
				var result turnload.RequestToolsResult
				testutil.FailErr(t, "decode selection", json.Unmarshal([]byte(raw), &result))
				if result.Discovery != nil || !activation.Active("healthy")["read"] {
					t.Fatalf("healthy response=%s", raw)
				}
			} else {
				reject := requireUnmatchedToolRequest(t, err)
				available, ok := reject.Data["available"].([]string)
				if !ok || len(available) != 1 || available[0] != "read" || raw != "" || len(activation.Active("healthy")) != 0 {
					t.Fatalf("no match output=%q rejection=%+v", raw, reject)
				}
			}
			if len(d.Ranks) != 1 {
				t.Fatalf("rank calls=%d", len(d.Ranks))
			}
		})
	}
}
