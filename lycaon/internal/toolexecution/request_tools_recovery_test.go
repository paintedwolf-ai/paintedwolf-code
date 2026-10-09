package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestRequestToolsRenderedRejectionKeepsClassification(t *testing.T) {
	formatter := guidance.NewStaticRejectFormatter(moduleHintConfig(t))
	err := tools.FormatRequestToolsReject(tools.RequestToolsDeps{RejectFmt: func() *guidance.StaticRejectFormatter { return formatter }}, "coordinator", "deliver the report", []string{"read"})
	reject := requireUnmatchedToolRequest(t, err)
	if reject.Data["need"] != "deliver the report" || !reflect.DeepEqual(reject.Data["available"], []string{"read"}) {
		t.Fatalf("request rejection lost recovery data: %+v", reject.Data)
	}
	if !strings.Contains(err.Error(), "Code: TOOL_REQUEST_UNMATCHED") || strings.Contains(err.Error(), toolrejection.ToolOwnerFailedCode) {
		t.Fatalf("rendered rejection disagrees with its structured code: %v", err)
	}
}

func TestRequestToolsRequiresLiveRegistration(t *testing.T) {
	for _, mode := range []string{"allowed", "wildcard", "all access", "immediate", "deferred", "removed"} {
		t.Run(mode, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			store := tools.NewMemoryActivation()
			name := "deliver_report"
			policy := fakeRequestBoundary{allowed: map[string]bool{name: true}}
			tctx := tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: "unregistered-" + mode,
					Agent: "coordinator"},
			}
			switch mode {
			case "wildcard":
				policy = fakeRequestBoundary{deferred: map[string]bool{"deliver_*": true}}
			case "all access":
				policy = fakeRequestBoundary{allowed: map[string]bool{name: true}, deferred: map[string]bool{name: true}}
				tctx.Turn.ToolAccess = sandbox.ToolAccessAll
			case "immediate":
				tctx.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools", name}, nil)
			case "deferred":
				tctx.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, []string{name})
			case "removed":
				name = "mcp_fixture_removed"
				registerRequestFixtureTools(t, reg, name)
				store.Activate(tctx.Identity.SessionID, []string{name}, name)
				testutil.FailErr(t, "remove tool provider", reg.ReplacePrefix("mcp_fixture_", nil, nil))
			}
			testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{Activation: store, Boundary: policy}).Register())
			before := store.Active(tctx.Identity.SessionID)
			_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": name}, tctx)
			reject := requireUnmatchedToolRequest(t, err)
			if reject.Data["need"] != name {
				t.Fatalf("need = %v, want %s", reject.Data["need"], name)
			}
			if !reflect.DeepEqual(before, store.Active(tctx.Identity.SessionID)) {
				t.Fatal("rejection changed activation state")
			}
		})
	}
}

func TestRequestToolsSkipsUnregisteredNamesInMixedRequest(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := tools.NewMemoryActivation()
	registerRequestFixtureTools(t, reg, "read")
	boundary := fakeRequestBoundary{allowed: map[string]bool{"read": true, "deliver_report": true}}
	testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{Activation: store, Boundary: boundary}).Register())
	out, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "deliver_report and read"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "mixed"},
	})
	testutil.FailErr(t, "request mixed tools", err)
	var result turnload.RequestToolsResult
	testutil.FailErr(t, "decode request result", json.Unmarshal([]byte(out), &result))
	if len(result.Loaded) != 0 || !reflect.DeepEqual(result.AlreadyLoaded, []string{"read"}) {
		t.Fatalf("unregistered name was not separated from available tools: %+v", result)
	}
}

func TestRequestToolsRecoveryListsOnlyLoadableTools(t *testing.T) {
	for _, mode := range []string{"profile", "compiled", "uncompiled surface", "no deferred tools"} {
		t.Run(mode, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			store := tools.NewMemoryActivation()
			boundary := fakeRequestBoundary{deferred: map[string]bool{"record_finding": true, "mcp_fixture_lookup": true}}
			testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{Activation: store, Boundary: boundary}).Register())
			handler := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
			testutil.FailErr(t, "register stock tool", reg.Register("record_finding", handler))
			testutil.FailErr(t, "register external tool", reg.RegisterDefinition(tools.Definition{
				Meta:     tools.ToolMeta{Name: "mcp_fixture_lookup", Source: tools.ToolSourceMCP, SourceID: "fixture", ArgsSchema: map[string]any{"type": "object"}},
				Contract: toolcontract.External("mcp:fixture"), Handler: handler,
			}))
			tctx := tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: "recovery",
					Agent: "coordinator"},
			}
			want := []string{"mcp_fixture_lookup"}
			switch mode {
			case "profile":
				want = append(want, "record_finding")
			case "compiled":
				tctx.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, want)
			case "uncompiled surface":
				tctx.Turn.TurnSurfaceID = "report"
			case "no deferred tools":
				tctx.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, nil)
				want = nil
			}
			output, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "frobnicate the widget"}, tctx)
			if len(want) == 0 {
				requireUnmatchedToolRequest(t, err)
			} else {
				testutil.FailErr(t, "discover tools", err)
				var result turnload.RequestToolsResult
				testutil.FailErr(t, "decode catalog", json.Unmarshal([]byte(output), &result))
				var available []string
				if result.Discovery != nil {
					for _, entry := range result.Discovery.Entries {
						available = append(available, entry.Name)
					}
				}
				if !slices.Equal(available, want) {
					t.Fatalf("recovery roster = %v, want %v", available, want)
				}
			}
			if store.Active(tctx.Identity.SessionID) != nil {
				t.Fatal("rejection changed activation state")
			}
			for _, name := range want {
				_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": name}, tctx)
				testutil.FailErr(t, "load advertised recovery tool "+name, err)
				if !store.Active(tctx.Identity.SessionID)[name] {
					t.Fatalf("advertised tool %s was not activated", name)
				}
			}
		})
	}
}

func requireUnmatchedToolRequest(t *testing.T, err error) *toolrejection.ToolReject {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_REQUEST_UNMATCHED" || toolrejection.HostRefusal(err) == nil {
		t.Fatalf("request rejection lost its structured classification: %v", err)
	}
	return reject
}

func TestRequestToolsActivatesDeclaredCompanionsOnly(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := tools.NewMemoryActivation()
	boundary := fakeRequestBoundary{deferred: map[string]bool{"write": true, "edit": true, "diff": true}}
	testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{Activation: store, Boundary: boundary,
		Resolve: func(_ context.Context, _ tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
			return turnload.RequestOutcome{Need: need, Exact: turnload.ExactNames(need, cards)}
		},
	}).Register())
	handler := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	testutil.FailErr(t, "register write", reg.Register("write", handler))
	testutil.FailErr(t, "register edit", reg.Register("edit", handler))
	testutil.FailErr(t, "register diff", reg.Register("diff", handler))

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "companions",
			Agent: "coordinator"},
		Turn: tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"write", "edit", "diff"})},
	}
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "write the new file"}, tctx)
	testutil.FailErr(t, "request write", err)

	// write declares edit as a companion; diff is neither selected nor declared.
	// replace_lines is a companion too, but this plan does not offer it.
	active := store.Active(tctx.Identity.SessionID)
	for _, tool := range []string{"write", "edit", "diff", "replace_lines"} {
		if active[tool] != (tool == "write" || tool == "edit") {
			t.Errorf("active %s = %v, want the selected tool and its offered companions", tool, active[tool])
		}
	}
}

func TestRequestCommandLoadsOnlyTheSelectedTool(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	activation := tools.NewMemoryActivation()
	names := []string{"command", "command_output", "command_stop", "git_checkout", "http_request", "process_list", "process_signal"}
	for _, name := range names {
		testutil.FailErr(t, "register "+name, reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }))
	}
	resolve := func(_ context.Context, _ tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
		return turnload.RequestOutcome{Need: need, Exact: turnload.ExactNames(need, cards)}
	}
	var recorded []string
	record := func(_ context.Context, tctx tools.ToolContext, _ turnload.RequestOutcome, result turnload.RequestToolsResult, _ time.Duration) {
		recorded = append([]string(nil), result.Loaded...)
		for _, name := range result.Loaded {
			if !activation.Active(tctx.Identity.SessionID)[name] {
				t.Errorf("recorded %s before activation", name)
			}
		}
	}
	testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{
		Activation: activation, Boundary: fakeRequestBoundary{}, Resolve: resolve, Record: record,
	}).Register())
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "command-family",
			Agent: "coordinator"},
		Turn: tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, names)},
	}
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "command"}, tctx)
	testutil.FailErr(t, "request command", err)
	if !slices.Equal(recorded, []string{"command"}) {
		t.Fatalf("recorded tools = %v, want only the selected command", recorded)
	}
	active := activation.Active(tctx.Identity.SessionID)
	for _, name := range names {
		want := name == "command"
		if active[name] != want {
			t.Errorf("active %s = %v, want %v", name, active[name], want)
		}
	}
	_, err = reg.Run(t.Context(), "request_tools", map[string]any{"need": "git_checkout"}, tctx)
	testutil.FailErr(t, "request Git separately", err)
	if !activation.Active(tctx.Identity.SessionID)["git_checkout"] {
		t.Fatal("removing a companion made the tool unrequestable")
	}
}

// The resolver the session wires ranks loadable schemas against the text;
// the tool loads only what it returns, and records the need.
func TestRequestToolsUsesTheWiredResolver(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := tools.NewMemoryActivation()
	registerRequestFixtureTools(t, reg, "git_compare", "http_request")
	var seenNeed string
	var seenCards []string
	resolve := func(_ context.Context, _ tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
		seenNeed = need
		for _, card := range cards {
			seenCards = append(seenCards, card.Name)
		}
		return turnload.RequestOutcome{Need: need, Ranked: map[string]float64{"git_compare": 3.5}}
	}
	testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{Activation: store, Boundary: fakeRequestBoundary{}, Resolve: resolve}).Register())
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "resolver",
			Agent: "coordinator"},
		Turn: tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"git_compare", "http_request"})},
	}
	out, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "compare the two branches"}, tctx)
	testutil.FailErr(t, "request", err)
	if seenNeed != "compare the two branches" || !slices.Equal(seenCards, []string{"git_compare", "http_request"}) {
		t.Fatalf("resolver saw need=%q cards=%v", seenNeed, seenCards)
	}
	var result turnload.RequestToolsResult
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &result))
	if !reflect.DeepEqual(result.Loaded, []string{"git_compare"}) || result.Need != "compare the two branches" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRequestReceiptRecordsOnlyFinalActivation(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	activation := tools.NewMemoryActivation()
	for _, name := range []string{"command", "git_commit"} {
		testutil.FailErr(t, "register "+name, reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }))
	}
	var recorded turnload.RequestToolsResult
	resolve := func(_ context.Context, _ tools.ToolContext, need string, _ []turnload.ToolCard) turnload.RequestOutcome {
		return turnload.RequestOutcome{Need: need, Exact: []string{"command", "git_commit"}}
	}
	record := func(_ context.Context, _ tools.ToolContext, _ turnload.RequestOutcome, result turnload.RequestToolsResult, _ time.Duration) {
		recorded = result
	}
	testutil.FailErr(t, "register request tools", tools.NewRequestTools(reg, tools.RequestToolsDeps{
		Activation: activation, Boundary: fakeRequestBoundary{}, Resolve: resolve, Record: record,
	}).Register())
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "filtered-receipt",
			Agent: "coordinator"},
		Turn: tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"command"})},
	}
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "run this"}, tctx)
	testutil.FailErr(t, "request with an out-of-surface result", err)
	if !slices.Equal(recorded.Loaded, []string{"command"}) || activation.Active(tctx.Identity.SessionID)["git_commit"] {
		t.Fatalf("receipt or activation included a filtered prediction: %+v", recorded)
	}
	_, err = reg.Run(t.Context(), "request_tools", map[string]any{"need": "run this again"}, tctx)
	testutil.FailErr(t, "request already active tool", err)
	if len(recorded.Loaded) != 0 || !slices.Equal(recorded.AlreadyLoaded, []string{"command"}) {
		t.Fatalf("repeat receipt = %+v", recorded)
	}
}

func TestRequestDiscoveryBindsLateProducersWithoutWideningTurnSurface(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	activation := tools.NewMemoryActivation()
	registerRequestFixtureTools(t, reg, "git_compare", "http_request")
	discovery := tools.NewRequestTools(reg, tools.RequestToolsDeps{Activation: activation, Boundary: fakeRequestBoundary{}})
	testutil.FailErr(t, "register discovery before session construction", discovery.Register())
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "late-producer", Agent: "coordinator"},
		Turn:     tools.InvocationTurn{TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"git_compare"})},
	}
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "compare revisions"}, tctx)
	testutil.FailErr(t, "unbound discovery", err)
	if len(activation.Active(tctx.Identity.SessionID)) != 0 {
		t.Fatal("unbound discovery activated an unnamed schema")
	}
	var recorded turnload.RequestToolsResult
	discovery.BindResolvers(func(_ context.Context, _ tools.ToolContext, need string, _ []turnload.ToolCard) turnload.RequestOutcome {
		return turnload.RequestOutcome{Need: need, Exact: []string{"git_compare", "http_request"}}
	}, func(_ context.Context, _ tools.ToolContext, _ turnload.RequestOutcome, result turnload.RequestToolsResult, _ time.Duration) {
		recorded = result
	})
	out, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": "compare revisions"}, tctx)
	testutil.FailErr(t, "invoke the registered handler after producer binding", err)
	var result turnload.RequestToolsResult
	testutil.FailErr(t, "decode activated surface", json.Unmarshal([]byte(out), &result))
	if !slices.Equal(result.Loaded, []string{"git_compare"}) || !slices.Equal(recorded.Loaded, result.Loaded) {
		t.Fatalf("late producer result = %#v, recorded = %#v", result, recorded)
	}
	if activation.Active(tctx.Identity.SessionID)["http_request"] {
		t.Fatal("late producer widened the compiled turn surface")
	}
}
