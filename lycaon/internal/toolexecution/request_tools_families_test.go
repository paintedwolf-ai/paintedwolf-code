package toolexecution

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestRequestToolsLoadsPermittedFamilies(t *testing.T) {
	cases := []struct {
		name, need, selected string
		available, want      []string
	}{
		{"search exact", "web_search", "", []string{"web_search", "fetch_url"}, []string{"fetch_url", "web_search"}},
		{"fetch exact", "fetch_url", "", []string{"web_search", "fetch_url"}, []string{"fetch_url", "web_search"}},
		{"ranked", "research the project", "web_search", []string{"web_search", "fetch_url"}, []string{"fetch_url", "web_search"}},
		{"terminal", "terminal_open", "", []string{"terminal_open", "terminal_read"}, []string{"terminal_open", "terminal_read"}},
		{"page", "page_open", "", []string{"page_open", "page_snapshot"}, []string{"page_open", "page_snapshot"}},
		{"restricted", "web_search", "", []string{"web_search"}, []string{"web_search"}},
	}
	for _, tc := range cases {
		for _, compiled := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/profile", true: "/compiled"}[compiled], func(t *testing.T) {
				reg := tools.NewDefaultRegistry()
				registerRequestFixtureTools(t, reg, "web_search", "fetch_url", "page_open", "page_snapshot", "terminal_open", "terminal_read")
				ledger := turnload.NewLedger()
				boundary := fakeRequestBoundary{deferred: map[string]bool{}}
				for _, name := range tc.available {
					boundary.deferred[name] = true
				}
				deps := tools.RequestToolsDeps{Activation: ledger, Boundary: boundary}
				if tc.selected != "" {
					deps.Resolve = func(_ context.Context, _ tools.ToolContext, need string, _ []turnload.ToolCard) turnload.RequestOutcome {
						return turnload.RequestOutcome{Need: need, Ranked: map[string]float64{tc.selected: .9}}
					}
				}
				testutil.FailErr(t, "register loader", tools.RegisterRequestTools(reg, deps))
				tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s"}}
				if compiled {
					tctx.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, tc.available)
				}
				for attempt := 0; attempt < 2; attempt++ {
					raw, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": tc.need}, tctx)
					testutil.FailErr(t, "request family", err)
					var result turnload.RequestToolsResult
					testutil.FailErr(t, "decode family", json.Unmarshal([]byte(raw), &result))
					got := result.Loaded
					if attempt == 1 {
						got = result.AlreadyLoaded
					}
					if !slices.Equal(got, tc.want) {
						t.Fatalf("result = %+v, want %v", result, tc.want)
					}
				}
				ledger.BeginTurn("s", turnload.TurnOpening{Loadable: tc.available, Boundary: turnload.Boundary{Cache: turnload.CacheCold}}, turnload.Decision{})
				if len(ledger.Active("s")) != len(tc.want) {
					t.Fatal("requested family lost at cold boundary")
				}
			})
		}
	}
}

func TestUnavailableFamilySeedCannotActivateItsSibling(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	registerRequestFixtureTools(t, reg, "web_search", "fetch_url")
	ledger := turnload.NewLedger()
	ledger.Activate("s", []string{"web_search"}, "previous surface")
	testutil.FailErr(t, "register loader", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{Activation: ledger, Boundary: fakeRequestBoundary{deferred: map[string]bool{"web_search": true, "fetch_url": true}}}))
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "web_search"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s"}, Turn: tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"fetch_url"})}})
	requireUnmatchedToolRequest(t, err)
	if ledger.Active("s")["fetch_url"] {
		t.Fatal("unavailable requested seed activated sibling")
	}
}

func TestRequestFamilyDoesNotLoadUnregisteredMember(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	registerRequestFixtureTools(t, reg, "web_search")
	ledger := turnload.NewLedger()
	testutil.FailErr(t, "register loader", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{Activation: ledger, Boundary: fakeRequestBoundary{}}))
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "web_search"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s"}, Turn: tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"web_search", "fetch_url"})}})
	testutil.FailErr(t, "load registered family member", err)
	if len(ledger.Active("s")) != 1 || !ledger.Active("s")["web_search"] {
		t.Fatalf("activation=%v", ledger.Active("s"))
	}
}
