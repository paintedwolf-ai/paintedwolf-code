package promptloop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

type recoveryCatalogBoundary struct{}

func (recoveryCatalogBoundary) AssertToolAllowed(context.Context, string, string, sandbox.ToolAccess) error {
	return nil
}
func (recoveryCatalogBoundary) ToolDeferred(string, string, sandbox.ToolAccess) bool { return true }

func discoveryRecoveryLoop(t *testing.T, reg tools.ToolRegistry, active func(string) map[string]bool) *PromptLoop {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	return NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{Tools: reg, LoadedTools: active}, Tools: ToolsDeps{BlockPlane: &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)}}})
}

func TestUnknownToolRecoveryPagesAndLoadsThroughProductionLoader(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	active := tools.NewMemoryActivation()
	var names []string
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("mcp_recovery_%02d", i)
		names = append(names, name)
		testutil.FailErr(t, "register catalog entry", reg.RegisterDefinition(tools.Definition{Meta: tools.ToolMeta{Name: name, Description: strings.Repeat("quoted \" data <tag> 界 ", 60), Source: tools.ToolSourceMCP, SourceID: "fixture", ArgsSchema: map[string]any{"type": "object"}}, Contract: toolcontract.External("mcp:fixture"), Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }}))
	}
	testutil.FailErr(t, "register loader", tools.RegisterRequestTools(reg, tools.RequestToolsDeps{Activation: active, Boundary: recoveryCatalogBoundary{}, Resolve: func(context.Context, tools.ToolContext, string, []turnload.ToolCard) turnload.RequestOutcome {
		t.Fatal("catalog navigation invoked ranker")
		return turnload.RequestOutcome{}
	}}))
	active.Activate("s", []string{names[0]}, "already loaded")
	allowed := append(slices.Clone(names[:24]), "mcp_unregistered")
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s", Agent: "coordinator"}, Turn: tools.InvocationTurn{TurnSurfaceID: "fixture", TurnOfferedToolNames: []string{"request_tools", names[0]}, TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, allowed)}}
	loop := discoveryRecoveryLoop(t, reg, active.Active)
	sess := &api.Session{ID: "s"}
	rejected := loop.Tools.rejectToolOccurrence(t.Context(), sess, api.ToolCall{Name: "create_file", Args: map[string]any{"payload": strings.Repeat("private", 10000)}}, tctx, "TOOL_NOT_OFFERED", nil)
	tr := toolrejection.AsToolReject(rejected)
	page, ok := tr.Data["discovery"].(*turnload.Discovery)
	if !ok || page.Total != 23 || page.NextNeed == "" || page.Status != "catalog" || page.Failure != "" {
		t.Fatalf("catalog = %+v", tr.Data)
	}
	if !strings.Contains(rejected.Copy["fix"], "discovery.entries") || !strings.Contains(rejected.Copy["fix"], "discovery.next_need") {
		t.Fatalf("recovery=%v", rejected.Copy)
	}
	beforeMetadata, err := json.Marshal(tr.Data)
	testutil.FailErr(t, "encode complete diagnostics", err)
	toolrejection.CompleteFailureMetadata(tr, "create_file", "")
	afterMetadata, err := json.Marshal(tr.Data)
	testutil.FailErr(t, "encode completed metadata", err)
	if string(beforeMetadata) != string(afterMetadata) {
		t.Fatal("recovery budget preceded ordinary metadata completion")
	}
	msg := loop.Tools.toolRejectMessage("create_file", "call", "assistant", nil, rejected)
	if len(msg.ToolResult.Feedback) != 1 {
		t.Fatalf("intrinsic feedback count=%d", len(msg.ToolResult.Feedback))
	}
	projected := transcript.Project([]api.Message{msg})
	var content string
	for _, p := range projected {
		if p.Role == api.MessageRoleTool {
			content += p.Content
		}
	}
	if !strings.Contains(content, `"status":"catalog"`) || strings.Contains(content, "details_omitted") || strings.Contains(content, "privateprivate") {
		t.Fatalf("provider lost safe catalog: %s", content)
	}
	if page.Instruction != "" || !strings.Contains(rejected.Copy["fix"], "`request_tools`") {
		t.Fatal("catalog must defer recovery instructions to rendered OAR")
	}
	pageBytes, err := json.Marshal(page)
	testutil.FailErr(t, "encode catalog page", err)
	if len(pageBytes) > turnload.DiscoveryPageBytes {
		t.Fatal("recovery page exceeded catalog bound")
	}
	firstCursor := page.NextNeed
	for _, mutate := range []func(*tools.ToolContext){
		func(c *tools.ToolContext) { c.Identity.SessionID = "other" },
		func(c *tools.ToolContext) { c.Identity.Agent = "worker" },
		func(c *tools.ToolContext) { c.Turn.TurnSurfaceID = "other" },
		func(c *tools.ToolContext) {
			c.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, names[:3])
		},
	} {
		changed := tctx
		mutate(&changed)
		if _, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": firstCursor}, changed); err == nil {
			t.Fatal("recovery cursor escaped caller/catalog binding")
		}
	}
	withEscalation := *rejected
	withEscalation.Facts = rejected.Facts.Merge(guidance.ToolResultFacts{}.WithFeedback("REPEATED_REJECTION", map[string]any{"detail": strings.Repeat("x", 40<<10)}, nil))
	escalated := loop.Tools.toolRejectMessage("create_file", "call", "assistant", nil, &withEscalation)
	if escalated.ToolResult.Feedback[0].Code != "TOOL_NOT_OFFERED" {
		t.Fatal("escalation displaced primary recovery")
	}
	for _, p := range transcript.Project([]api.Message{escalated}) {
		if p.Role == api.MessageRoleTool && !strings.Contains(p.Content, `"status":"catalog"`) {
			t.Fatal("later feedback displaced first catalog")
		}
	}
	var seen []string
	for {
		for _, entry := range page.Entries {
			seen = append(seen, entry.Name)
		}
		if page.NextNeed == "" {
			break
		}
		raw, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": page.NextNeed}, tctx)
		testutil.FailErr(t, "page recovery catalog", err)
		var result turnload.RequestToolsResult
		testutil.FailErr(t, "decode page", json.Unmarshal([]byte(raw), &result))
		page = result.Discovery
		if page == nil {
			t.Fatal("continuation lost catalog")
		}
	}
	if !slices.Equal(seen, names[1:24]) {
		t.Fatalf("catalog leaked/omitted names: %v", seen)
	}
	_, err = reg.Run(t.Context(), "request_tools", map[string]any{"need": names[23]}, tctx)
	testutil.FailErr(t, "activate exact selected name", err)
	if !active.Active("s")[names[23]] || len(active.Active("s")) != 2 {
		t.Fatal("exact recovery changed unrelated activation")
	}
	if _, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": firstCursor}, tctx); err == nil {
		t.Fatal("recovery cursor survived changed active catalog")
	}
}

func TestUnknownToolDiscoveryGuidanceRequiresProjectedCurrentCatalog(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register write", reg.Register("write", func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }))
	for _, tc := range []struct {
		name  string
		setup func(*tools.ToolContext, *ContextDeps, *api.ToolCall)
		want  bool
	}{
		{name: "last page", want: true},
		{name: "known deferred", setup: func(_ *tools.ToolContext, _ *ContextDeps, call *api.ToolCall) { call.Name = "write" }},
		{name: "no session identity", setup: func(ctx *tools.ToolContext, _ *ContextDeps, _ *api.ToolCall) { ctx.Identity.SessionID = "" }},
		{name: "other session", setup: func(ctx *tools.ToolContext, _ *ContextDeps, _ *api.ToolCall) { ctx.Identity.SessionID = "other" }},
		{name: "no profile identity", setup: func(ctx *tools.ToolContext, _ *ContextDeps, _ *api.ToolCall) { ctx.Identity.Agent = "" }},
		{name: "unknown activation", setup: func(_ *tools.ToolContext, deps *ContextDeps, _ *api.ToolCall) { deps.LoadedTools = nil }},
		{name: "unknown plan", setup: func(ctx *tools.ToolContext, _ *ContextDeps, _ *api.ToolCall) {
			ctx.Turn.TurnToolPlan = toolsurface.Plan{}
		}},
		{name: "no loader", setup: func(ctx *tools.ToolContext, _ *ContextDeps, _ *api.ToolCall) {
			ctx.Turn.TurnOfferedToolNames = []string{"read"}
		}},
		{name: "no unloaded entries", setup: func(_ *tools.ToolContext, deps *ContextDeps, _ *api.ToolCall) {
			deps.LoadedTools = func(string) map[string]bool { return map[string]bool{"write": true} }
		}},
		{name: "oversized rejected identifier", setup: func(_ *tools.ToolContext, _ *ContextDeps, call *api.ToolCall) {
			call.Name = strings.Repeat("<界>\"", 12000)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop := discoveryRecoveryLoop(t, reg, func(string) map[string]bool { return nil })
			ctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s", Agent: "coordinator"}, Turn: tools.InvocationTurn{TurnOfferedToolNames: []string{"request_tools"}, TurnToolPlan: toolsurface.Compile([]string{"request_tools"}, []string{"write"})}}
			call := api.ToolCall{Name: "create_file"}
			if tc.setup != nil {
				tc.setup(&ctx, &loop.Context.Deps, &call)
			}
			reject := loop.Tools.rejectToolOccurrence(t.Context(), &api.Session{ID: "s"}, call, ctx, "TOOL_NOT_OFFERED", nil)
			tr := toolrejection.AsToolReject(reject)
			page, hasPage := tr.Data["discovery"].(*turnload.Discovery)
			if hasPage && strings.Contains(page.Instruction, "next_need") {
				t.Fatal("last page instruction promised continuation")
			}
			if hasPage != tc.want || strings.Contains(reject.Copy["fix"], "discovery.entries") != tc.want {
				t.Fatalf("page=%v fix=%s", hasPage, reject.Copy["fix"])
			}
			if strings.Contains(reject.Copy["fix"], "discovery.next_need") {
				t.Fatal("last or missing page promised continuation")
			}
			msg := loop.Tools.toolRejectMessage(call.Name, "call", "assistant", nil, reject)
			for _, p := range transcript.Project([]api.Message{msg}) {
				if tc.want && p.Role == api.MessageRoleTool && (strings.Contains(p.Content, "details_omitted") || !strings.Contains(p.Content, `"name":"write"`)) {
					t.Fatal("actionable page dropped at projection")
				}
			}
		})
	}
}
