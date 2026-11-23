package loading

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
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
		Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command", "page_open"}},
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
		Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"command", "page_open", "measure_page"}},
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
					Turn: tools.InvocationTurn{
						TurnOfferedToolNames:   []string{"verify"},
						TurnOfferedToolSchemas: map[string]map[string]any{"verify": schema},
					},
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

// Verifies deferred-only tools do not admit skills on tool events and reader errors omit guidance.
func TestReadSkillForToolPreservesDeferredOfferDistinctionAndOmitsOnReaderFailure(t *testing.T) {
	roster := []skills.Skill{{
		Name:        "review-accessibility",
		Description: "Audit a page for accessibility.",
		Body:        "Use `measure_page` and `page_open` ({% if profile_has_measure_page %}ready{% else %}missing{% endif %}).",
	}}
	plan := toolsurface.Compile([]string{"command", "request_tools"}, []string{"page_open", "measure_page"})

	newService := func(sks []skills.Skill) *Service {
		m := &Service{
			Ledger:  turnload.NewLedger(),
			decider: &decidetest.Fake{Scores: []float64{3.8}},
			skills: func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
				return sks, nil
			},
		}
		readTool := &skilltools.SkillsReadTool{
			TemplateVars: func(ctx context.Context, tctx tools.ToolContext) map[string]any {
				r, err := m.toolEventReader(ctx, &api.Session{ID: tctx.Identity.SessionID}, tctx, "page_open", 1)
				testutil.FailErr(t, "toolEventReader", err)
				vars, err := prompts.SkillVars(r)
				testutil.FailErr(t, "SkillVars", err)
				return vars
			},
		}
		m.SetSkillBodyRenderer(readTool.RenderBody)
		return m
	}

	// Authoritative TurnOfferedToolNames: measure_page is in DeferredNames + Loadable, not offered.
	mAuth := newService(roster)
	sessAuth := &api.Session{ID: "s-auth"}
	mAuth.Ledger.BeginTurn(sessAuth.ID, turnload.TurnOpening{
		OpeningMessageID: "m-deferred",
		Request:          "Audit accessibility",
		Loadable:         []string{"page_open", "measure_page"},
	}, turnload.Decision{})
	mAuth.ObserveToolCall(t.Context(), sessAuth, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessAuth.ID},
		Turn: tools.InvocationTurn{
			TurnToolPlan:         plan,
			TurnOfferedToolNames: []string{"command", "request_tools", "page_open"},
		},
	}, "page_open")
	if got := mAuth.SkillPreload(sessAuth.ID); got != nil {
		t.Fatalf("deferred-only measure_page falsely preloaded skill: %+v", got)
	}
	if ptr := mAuth.SkillPointer(sessAuth.ID); ptr != nil {
		t.Fatalf("deferred-only measure_page set pointer: %+v", ptr)
	}

	// Paired offered case with authoritative TurnOfferedToolNames.
	mAuth.Ledger.BeginTurn(sessAuth.ID, turnload.TurnOpening{
		OpeningMessageID: "m-offered",
		Request:          "Audit accessibility",
		Loadable:         []string{"page_open", "measure_page"},
	}, turnload.Decision{})
	mAuth.ObserveToolCall(t.Context(), sessAuth, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessAuth.ID},
		Turn: tools.InvocationTurn{
			TurnToolPlan:         plan,
			TurnOfferedToolNames: []string{"command", "request_tools", "page_open", "measure_page"},
		},
	}, "page_open")
	if got := mAuth.SkillPreload(sessAuth.ID); got == nil || got.Name != "review-accessibility" || got.Body != "Use `measure_page` and `page_open` (ready)." {
		t.Fatalf("offered measure_page preload = %+v", got)
	}

	// Nil TurnOfferedToolNames: unactivated deferred measure_page stays unheld until activated.
	mPlan := newService(roster)
	sessPlan := &api.Session{ID: "s-plan"}
	mPlan.Ledger.BeginTurn(sessPlan.ID, turnload.TurnOpening{
		OpeningMessageID: "m-plan-deferred",
		Request:          "Audit accessibility",
		Loadable:         []string{"page_open", "measure_page"},
	}, turnload.Decision{})
	mPlan.ObserveToolCall(t.Context(), sessPlan, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessPlan.ID},
		Turn:     tools.InvocationTurn{TurnToolPlan: plan},
	}, "page_open")
	if got := mPlan.SkillPreload(sessPlan.ID); got != nil {
		t.Fatalf("unactivated deferred measure_page preloaded skill: %+v", got)
	}

	mPlan.Ledger.BeginTurn(sessPlan.ID, turnload.TurnOpening{
		OpeningMessageID: "m-plan-activated",
		Request:          "Audit accessibility",
		Loadable:         []string{"page_open", "measure_page"},
	}, turnload.Decision{})
	mPlan.Ledger.Activate(sessPlan.ID, []string{"measure_page"}, "measure page geometry")
	mPlan.ObserveToolCall(t.Context(), sessPlan, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessPlan.ID},
		Turn:     tools.InvocationTurn{TurnToolPlan: plan},
	}, "page_open")
	if got := mPlan.SkillPreload(sessPlan.ID); got == nil || got.Name != "review-accessibility" || got.Body != "Use `measure_page` and `page_open` (ready)." {
		t.Fatalf("activated measure_page preload = %+v", got)
	}

	// Coordinator surface branch (TurnSurfaceID != "") preserves TurnOfferedToolNames vs surface deferred set.
	surfReader, err := mAuth.toolEventReader(t.Context(), sessAuth, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "coordinator", SessionID: sessAuth.ID},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: []string{"command", "request_tools"},
		},
	}, "command", 1)
	testutil.FailErr(t, "surface toolEventReader", err)
	if len(surfReader.Requestable()) == 0 {
		t.Fatal("expected deferred surface tools to remain requestable")
	}

	// Reader failure: unknown profile omits prose-only stock skill guidance.
	mFail := newService([]skills.Skill{{
		Name:        "release-checklist",
		Description: "Verify release readiness.",
		Body:        "Check every release artifact before closing.",
	}})
	sessFail := &api.Session{ID: "s-fail"}
	mFail.Ledger.BeginTurn(sessFail.ID, turnload.TurnOpening{
		OpeningMessageID: "m-fail",
		Request:          "Prepare release",
		Loadable:         []string{"page_open"},
	}, turnload.Decision{})
	mFail.ObserveToolCall(t.Context(), sessFail, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "unknown-profile", SessionID: sessFail.ID},
		Turn:     tools.InvocationTurn{TurnOfferedToolNames: []string{"page_open"}},
	}, "page_open")
	if got := mFail.SkillPreload(sessFail.ID); got != nil {
		t.Fatalf("reader failure preloaded unestablished skill: %+v", got)
	}
	if ptr := mFail.SkillPointer(sessFail.ID); ptr != nil {
		t.Fatalf("reader failure set pointer for unestablished skill: %+v", ptr)
	}
}
