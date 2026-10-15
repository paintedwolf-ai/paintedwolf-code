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
	msg := loop.Tools.toolRejectMessage("create_file", "call", "assistant", nil, rejected)
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
	_, err := reg.Run(t.Context(), "request_tools", map[string]any{"need": names[23]}, tctx)
	testutil.FailErr(t, "activate exact selected name", err)
	if !active.Active("s")[names[23]] || len(active.Active("s")) != 2 {
		t.Fatal("exact recovery changed unrelated activation")
	}
}
