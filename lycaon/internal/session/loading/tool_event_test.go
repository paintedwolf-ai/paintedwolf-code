package loading

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReadSkillForToolGatesStockSkillsByOfferedToolsAndCopiesResources(t *testing.T) {
	roster := []skills.Skill{
		{
			Name:        "review-accessibility",
			Description: "Audit a page for accessibility.",
			Body:        "Use `measure_page` and `page_open` to inspect the page, and optionally {% if profile_has_capture_page %}`capture_page`{% endif %}.",
			Resources:   []string{"references/wcag.md"},
		},
	}
	sess := &api.Session{ID: "s1"}
	ledger := turnload.NewLedger()
	m := &Service{
		Ledger:  ledger,
		decider: &decidetest.Fake{Scores: []float64{3.8}},
		skills: func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
			return roster, nil
		},
	}
	m.SetSkillBodyRenderer(func(_ context.Context, _ tools.ToolContext, sk skills.Skill) (string, error) {
		return sk.Body, nil
	})

	// Turn 1: required `measure_page` is not offered on the turn.
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{
		OpeningMessageID: "m1",
		Request:          "Open the web page",
		Loadable:         []string{"page_open"},
	}, turnload.Decision{})
	m.ObserveToolCall(t.Context(), sess, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Turn:     requestTurn(t, toolsurface.Compile([]string{"command", "page_open"}, nil), nil),
	}, "page_open")
	if got := m.SkillPreload(sess.ID); got != nil {
		t.Fatalf("preloaded stock skill without required tool: %+v", got)
	}
	if ptr := m.SkillPointer(sess.ID); ptr != nil {
		t.Fatalf("pointed to stock skill without required tool: %+v", ptr)
	}

	// Turn 2: required tools are offered while optional `capture_page` is absent.
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{
		OpeningMessageID: "m2",
		Request:          "Audit the web page accessibility",
		Loadable:         []string{"page_open", "measure_page"},
	}, turnload.Decision{})
	m.ObserveToolCall(t.Context(), sess, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Turn:     requestTurn(t, toolsurface.Compile([]string{"command", "page_open", "measure_page"}, nil), nil),
	}, "page_open")
	got := m.SkillPreload(sess.ID)
	if got == nil || got.Name != "review-accessibility" || got.Tool != "page_open" {
		t.Fatalf("expected review-accessibility preload on tool event, got %+v", got)
	}
	if len(got.Resources) != 1 || got.Resources[0] != "references/wcag.md" {
		t.Fatalf("preload resources = %v, want [references/wcag.md]", got.Resources)
	}
}

// Tool-event skill selection must honor projected schemas in both wide->narrow and narrow->wide orders.
func TestReadSkillForToolDistinguishesProjectedSchemasBothOrders(t *testing.T) {
	wideVerify := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"capability_request": map[string]any{
				"type":       "object",
				"properties": map[string]any{"host_resources": map[string]any{"type": "array"}},
			},
		},
	}
	narrowVerify := map[string]any{
		"type":       "object",
		"properties": map[string]any{"socks_proxy": map[string]any{"type": "boolean"}},
	}
	roster := []skills.Skill{{
		Name:        "deploy-to-a-hosting-platform",
		Description: "Deploy with host CLI access.",
		Body:        "Run `verify`.",
		Metadata:    map[string]string{"host_resources": "cloud-cli"},
	}}
	for _, order := range []struct {
		name      string
		wideFirst bool
	}{
		{"wide_then_narrow", true},
		{"narrow_then_wide", false},
	} {
		t.Run(order.name, func(t *testing.T) {
			sess := &api.Session{ID: "s-" + order.name}
			ledger := turnload.NewLedger()
			m := &Service{
				Ledger:  ledger,
				decider: &decidetest.Fake{Scores: []float64{3.8}},
				skills: func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
					return roster, nil
				},
			}
			m.SetSkillBodyRenderer(func(_ context.Context, _ tools.ToolContext, sk skills.Skill) (string, error) {
				return sk.Body, nil
			})
			step := func(msgID string, wide bool) {
				schema := narrowVerify
				if wide {
					schema = wideVerify
				}
				ledger.BeginTurn(sess.ID, turnload.TurnOpening{
					OpeningMessageID: msgID,
					Request:          "Deploy the app",
					Loadable:         []string{"verify"},
				}, turnload.Decision{})
				m.ObserveToolCall(t.Context(), sess, tools.ToolContext{
					Identity: tools.InvocationIdentity{Agent: "coordinator"},
					Turn:     requestTurn(t, toolsurface.Compile([]string{"verify"}, nil), map[string]map[string]any{"verify": schema}),
				}, "verify")
				got := m.SkillPreload(sess.ID)
				if wide && (got == nil || got.Name != "deploy-to-a-hosting-platform") {
					t.Fatalf("wide verify missed preload: %+v", got)
				}
				if !wide && got != nil {
					t.Fatalf("narrow verify without host_resources preloaded %+v", got)
				}
			}
			step("m1", order.wideFirst)
			step("m2", !order.wideFirst)
		})
	}
}

// A tool event reads the reach of the request that carried the call: a
// deferred tool the request did not offer admits no skill, and a call without
// a request Reader preloads nothing.
func TestReadSkillForToolReadsTheCarryingRequest(t *testing.T) {
	roster := []skills.Skill{{
		Name:        "review-accessibility",
		Description: "Audit a page for accessibility.",
		Body:        "Use `measure_page` and `page_open` ({% if profile_has_measure_page %}ready{% else %}missing{% endif %}).",
	}}
	m := &Service{
		Ledger:  turnload.NewLedger(),
		decider: &decidetest.Fake{Scores: []float64{3.8}},
		skills: func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
			return roster, nil
		},
	}
	readTool := &skilltools.SkillsReadTool{
		TemplateVars: func(_ context.Context, tctx tools.ToolContext) map[string]any {
			vars, err := prompts.SkillVars(tctx.Turn.Reader)
			testutil.FailErr(t, "SkillVars", err)
			return vars
		},
	}
	m.SetSkillBodyRenderer(readTool.RenderBody)
	sess := &api.Session{ID: "s-request"}
	for _, tc := range []struct {
		name string
		turn tools.InvocationTurn
		want bool
	}{
		{"deferred", requestTurn(t, toolsurface.Compile([]string{"command", "request_tools", "page_open"}, []string{"measure_page"}), nil), false},
		{"offered", requestTurn(t, toolsurface.Compile([]string{"command", "request_tools", "page_open", "measure_page"}, nil), nil), true},
		{"no reader", tools.InvocationTurn{TurnOfferedToolNames: []string{"page_open", "measure_page"}}, false},
	} {
		m.Ledger.BeginTurn(sess.ID, turnload.TurnOpening{OpeningMessageID: "m-" + tc.name, Request: "Audit accessibility", Loadable: []string{"page_open", "measure_page"}}, turnload.Decision{})
		m.ObserveToolCall(t.Context(), sess, tools.ToolContext{
			Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sess.ID},
			Turn:     tc.turn,
		}, "page_open")
		got := m.SkillPreload(sess.ID)
		if (got != nil) != tc.want {
			t.Fatalf("%s: preload = %+v, want preloaded %v", tc.name, got, tc.want)
		}
		if tc.want && got.Body != "Use `measure_page` and `page_open` (ready)." {
			t.Fatalf("%s: preload body = %q", tc.name, got.Body)
		}
	}
}

// requestTurn is a coordinator request carrying plan's immediate tools.
func requestTurn(t *testing.T, plan toolsurface.Plan, schemas map[string]map[string]any) tools.InvocationTurn {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true}, prompts.TurnProjection{
		ProfileID:        prompts.CoordinatorProfileID,
		Plan:             plan,
		Offered:          plan.ImmediateNames(),
		ProjectedSchemas: schemas,
	})
	testutil.FailErr(t, "request reader", err)
	return tools.InvocationTurn{
		TurnToolPlan:           plan,
		TurnOfferedToolNames:   plan.ImmediateNames(),
		TurnOfferedToolSchemas: schemas,
		Reader:                 reader,
	}
}
