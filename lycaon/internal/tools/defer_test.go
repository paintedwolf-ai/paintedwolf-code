package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestMemoryActivationRoundTrip(t *testing.T) {
	store := NewMemoryActivation()
	if got := store.Active("s1"); got != nil {
		t.Fatalf("Active before activation = %v", got)
	}
	store.Activate("s1", []string{"wc", "chmod"}, "count and fix modes")
	store.Activate("s1", []string{"wc"}, "")
	got := store.Active("s1")
	if !got["wc"] || !got["chmod"] || len(got) != 2 {
		t.Fatalf("Active = %v", got)
	}
	if store.Active("s2") != nil {
		t.Fatal("sessions must be isolated")
	}
}

func TestHideSkillsReadWhenEmpty(t *testing.T) {
	metas := []ToolMeta{{Name: "read"}, {Name: "skills_read"}, {Name: "grep"}}
	kept := HideSkillsReadWhenEmpty(metas, 2)
	if len(kept) != 3 {
		t.Fatalf("non-empty catalog hid tools: %d", len(kept))
	}
	hidden := HideSkillsReadWhenEmpty(metas, 0)
	var names []string
	for _, meta := range hidden {
		names = append(names, meta.Name)
	}
	if len(names) != 2 || names[0] != "read" || names[1] != "grep" {
		t.Fatalf("empty catalog tools = %v", names)
	}
}

func TestFilterDeferredMetas(t *testing.T) {
	metas := []ToolMeta{
		{Name: "read"},
		{Name: "wc", Deferred: true},
		{Name: "chmod", Deferred: true},
	}
	got := FilterDeferredMetas(metas, map[string]bool{"chmod": true})
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	want := []string{"read", "chmod"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("filtered = %v want %v", names, want)
	}
}

type fakeRequestBoundary struct {
	deferred map[string]bool
	allowed  map[string]bool
}

func (f fakeRequestBoundary) AssertToolAllowed(_ context.Context, _, toolName string, _ sandbox.ToolAccess) error {
	if f.allowed[toolName] {
		return nil
	}
	return context.Canceled
}

func (f fakeRequestBoundary) ToolDeferred(_, toolName string, _ sandbox.ToolAccess) bool {
	if f.deferred[toolName] {
		return true
	}
	for pattern := range f.deferred {
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(toolName, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

func TestRequestToolsLoadsNamedDeferredTools(t *testing.T) {
	reg := NewDefaultRegistry()
	registerRequestFixtureTools(t, reg, "wc", "read")
	store := NewMemoryActivation()
	err := RegisterRequestTools(reg, RequestToolsDeps{
		Activation: store,
		Boundary: fakeRequestBoundary{
			deferred: map[string]bool{"wc": true, "chmod": true},
			allowed:  map[string]bool{"read": true, "wc": true, "chmod": true},
		},
	})
	testutil.FailErr(t, "RegisterRequestTools", err)

	out, err := reg.Run(context.Background(), "request_tools",
		map[string]any{"need": "wc and read the file"},
		ToolContext{SessionID: "s1", Agent: "implement"})
	testutil.FailErr(t, "request_tools run", err)

	var result turnload.RequestToolsResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &result))
	if !reflect.DeepEqual(result.Loaded, []string{"wc"}) {
		t.Fatalf("loaded = %v", result.Loaded)
	}
	if !reflect.DeepEqual(result.AlreadyLoaded, []string{"read"}) {
		t.Fatalf("already_loaded = %v", result.AlreadyLoaded)
	}
	if !store.Active("s1")["wc"] {
		t.Fatal("wc must be activated for session")
	}
}

// Without a wired resolver, only explicit schema names load.
func TestRequestToolsWithoutResolverNeedsExactName(t *testing.T) {
	reg := NewDefaultRegistry()
	store := NewMemoryActivation()
	boundary := fakeRequestBoundary{
		deferred: map[string]bool{"mcp_tracker_search_issues": true, "mcp_tracker_create_issue": true},
		allowed:  map[string]bool{"mcp_tracker_search_issues": true, "mcp_tracker_create_issue": true},
	}
	testutil.FailErr(t, "RegisterRequestTools", RegisterRequestTools(reg, RequestToolsDeps{
		Activation: store,
		Boundary:   boundary,
	}))
	for _, meta := range []ToolMeta{
		{Name: "mcp_tracker_search_issues", Description: "Search project issues", Source: ToolSourceMCP, SourceID: "tracker", ArgsSchema: map[string]any{"type": "object"}},
		{Name: "mcp_tracker_create_issue", Description: "Create a project issue", Source: ToolSourceMCP, SourceID: "tracker", ArgsSchema: map[string]any{"type": "object"}},
	} {
		testutil.FailErr(t, "register mcp tool", reg.RegisterDefinition(Definition{
			Meta: meta, Contract: toolcontract.External("mcp:tracker"),
			Handler: func(context.Context, map[string]any, ToolContext) (string, error) { return "", nil },
		}))
	}

	discovery, err := reg.Run(context.Background(), "request_tools",
		map[string]any{"need": "search the tracker issues"},
		ToolContext{SessionID: "s1", Agent: "implement"})
	testutil.FailErr(t, "discover without resolver", err)
	var page turnload.RequestToolsResult
	testutil.FailErr(t, "decode discovery", json.Unmarshal([]byte(discovery), &page))
	if page.Discovery == nil || page.Discovery.Status != "ranking_unavailable" || len(page.Discovery.Entries) != 2 || len(store.Active("s1")) != 0 {
		t.Fatalf("discovery = %+v", page)
	}
	out, err := reg.Run(context.Background(), "request_tools",
		map[string]any{"need": "mcp_tracker_search_issues"},
		ToolContext{SessionID: "s1", Agent: "implement"})
	testutil.FailErr(t, "request_tools exact name", err)
	var result turnload.RequestToolsResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &result))
	if len(result.Loaded) == 0 || result.Loaded[0] != "mcp_tracker_search_issues" {
		t.Fatalf("loaded = %v", result.Loaded)
	}
}

func TestRequestToolsLoadsSurfaceDeferred(t *testing.T) {
	reg := NewDefaultRegistry()
	registerRequestFixtureTools(t, reg, "page_open", "read")
	store := NewMemoryActivation()
	err := RegisterRequestTools(reg, RequestToolsDeps{
		Activation: store,
		Boundary: fakeRequestBoundary{
			allowed: map[string]bool{"read": true, "page_open": true},
		},
	})
	testutil.FailErr(t, "RegisterRequestTools", err)

	out, err := reg.Run(context.Background(), "request_tools",
		map[string]any{"need": "page_open then read"},
		ToolContext{
			SessionID:     "s1",
			Agent:         "coordinator",
			TurnSurfaceID: "implement_investigate",
			TurnToolPlan:  toolsurface.Compile([]string{"read", "request_tools"}, []string{"page_open"}),
		})
	testutil.FailErr(t, "request_tools run", err)

	var result turnload.RequestToolsResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &result))
	if !reflect.DeepEqual(result.Loaded, []string{"page_open"}) {
		t.Fatalf("loaded = %v", result.Loaded)
	}
	if !reflect.DeepEqual(result.AlreadyLoaded, []string{"read"}) {
		t.Fatalf("already_loaded = %v", result.AlreadyLoaded)
	}
}

// The three command job controls can be requested together by exact name.
func TestRequestToolsLoadsExplicitControlFamily(t *testing.T) {
	reg := NewDefaultRegistry()
	registerRequestFixtureTools(t, reg, "command", "command_output", "command_stop")
	store := NewMemoryActivation()
	testutil.FailErr(t, "RegisterRequestTools", RegisterRequestTools(reg, RequestToolsDeps{
		Activation: store,
		Boundary:   fakeRequestBoundary{allowed: map[string]bool{"command": true}},
	}))

	family := []string{"command", "command_output", "command_stop"}
	out, err := reg.Run(context.Background(), "request_tools",
		map[string]any{"need": "command command_output command_stop"},
		ToolContext{
			SessionID:     "s1",
			Agent:         "coordinator",
			TurnSurfaceID: "implement_investigate",
			TurnToolPlan:  toolsurface.Compile([]string{"request_tools"}, family),
		})
	testutil.FailErr(t, "request_tools run", err)

	var result turnload.RequestToolsResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &result))
	if !reflect.DeepEqual(result.Loaded, family) {
		t.Fatalf("loaded = %v want the whole family", result.Loaded)
	}
	for _, name := range family {
		if !store.Active("s1")[name] {
			t.Fatalf("%s not activated for the session", name)
		}
	}
}

// registerRequestFixtureTools registers no-op tools under names; an mcp_
// name registers as an external definition so provider replacement can
// remove it.
func registerRequestFixtureTools(t *testing.T, reg *DefaultRegistry, names ...string) {
	t.Helper()
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "", nil }
	for _, name := range names {
		if strings.HasPrefix(name, "mcp_") {
			testutil.FailErr(t, "register "+name, reg.RegisterDefinition(Definition{
				Meta:     ToolMeta{Name: name, Source: ToolSourceMCP, SourceID: "fixture", ArgsSchema: map[string]any{"type": "object"}},
				Contract: toolcontract.External("mcp:fixture"), Handler: handler,
			}))
			continue
		}
		testutil.FailErr(t, "register "+name, reg.Register(name, handler))
	}
}
