package anchor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubTemplateEngine struct {
	prompts.PromptTemplateEngine
	templates map[string]string
	calls     []string
}

func (s *stubTemplateEngine) RenderKick(_ context.Context, name string, _ map[string]any) (string, error) {
	s.calls = append(s.calls, name)
	return s.templates[name], nil
}

func TestStockLifecycleHintMappingsAndDelivery(t *testing.T) {
	reg := loadTestRegistry(t)
	kicks := &kick.KickEngine{}
	// Deliberately omit a template engine: stock hint delivery must not need Pongo.
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(reg)

	cases := []struct {
		anchorID anchor.ID
		render   string
		hintCode string
		surface  string
		wantSub  string
	}{
		{anchor.AuthzSealFailed, "coordinator-authz-seal-failed", "COORDINATOR_APPROVAL_SEAL_FAILED", "coordinator", "[host:coordinator-authz-seal-failed]"},
		{anchor.ComposeDone, "coordinator-compose-done", "WORKFLOW_COMPOSE_SUCCEEDED", "coordinator", "paraphrase `coordinator_brief`"},
		{anchor.EditFollowUpRepeat, "coordinator-edit-follow-up-repeat", "COORDINATOR_EDIT_FOLLOWUP_REPEATED", "coordinator", "Second consecutive user follow-up"},
		{anchor.FeedbackPending, "coordinator-feedback-pending", "WORKFLOW_FEEDBACK_CARD_PENDING", "coordinator", "**pending_feedback**"},
		{anchor.FeedbackReceived, "coordinator-feedback-received", "WORKFLOW_FEEDBACK_ANSWER_RECEIVED", "coordinator", "Prose is not the answer."},
		{anchor.ReviewLoopContinue, "coordinator-review-loop-continue", "WORKFLOW_REVIEW_ROUND_CONTINUES", "phase", "`submit_verdict`"},
		{anchor.ReviewLoopDecide, "coordinator-review-loop-decide", "WORKFLOW_REVIEW_CAP_REACHED", "phase", "terminal verdict"},
		{anchor.TurnSpendSoftStop, "coordinator-spend-soft-stop", "COORDINATOR_SPEND_WIND_DOWN", "coordinator", "[host:coordinator-spend-soft-stop]"},
	}

	for _, tc := range cases {
		t.Run(string(tc.anchorID), func(t *testing.T) {
			b, ok := reg.Inform(tc.anchorID)
			if !ok || b == nil {
				t.Fatalf("missing primary inform binding for %s", tc.anchorID)
			}
			if b.Render != tc.render || b.HintCode != tc.hintCode {
				t.Fatalf("binding = (render=%q hint_code=%q), want (%q, %q)", b.Render, b.HintCode, tc.render, tc.hintCode)
			}
			wantMsg, ok := reg.HintMessage(tc.hintCode)
			if !ok || !strings.Contains(wantMsg, tc.wantSub) {
				t.Fatalf("HintMessage(%s) = (%q, %v), want substring %q", tc.hintCode, wantMsg, ok, tc.wantSub)
			}

			// Emit via bus and verify queued kick identity and rendered message.
			sessID := "sess-" + string(tc.anchorID)
			bus.Emit(t.Context(), sessID, tc.anchorID, anchor.Envelope{})
			kickID := kicks.TakePendingKickIDUnless(sessID, nil)
			if kickID != tc.render {
				t.Fatalf("queued kick id = %q, want %q", kickID, tc.render)
			}
			text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), sessID, kick.CoordinatorKickRenderContext{})
			testutil.FailErr(t, "RenderPendingNudge", err)
			if !ok || text != wantMsg {
				t.Fatalf("rendered = (%q, %v), want %q", text, ok, wantMsg)
			}
			kicks.AckPendingNudge(sessID, lease)

			// Pre-change stem-only queued stock entry resolves via session bound catalog.
			stemSess := "stem-" + string(tc.anchorID)
			kicks.QueueDeferred(stemSess, tc.render)
			if id := kicks.TakePendingKickIDUnless(stemSess, nil); id != tc.render {
				t.Fatalf("stem queued id = %q, want %q", id, tc.render)
			}
			stemText, stemLease, stemOK, err := kicks.RenderPendingNudge(t.Context(), stemSess, kick.CoordinatorKickRenderContext{})
			testutil.FailErr(t, "RenderPendingNudge stem-only", err)
			if !stemOK || stemText != wantMsg {
				t.Fatalf("stem-only rendered = (%q, %v), want %q", stemText, stemOK, wantMsg)
			}
			kicks.AckPendingNudge(stemSess, stemLease)
		})
	}

	// Dynamic low-runway template must remain a template binding (no hint_code).
	runway, ok := reg.Inform(anchor.TurnSpendRunwayLow)
	if !ok || runway == nil || runway.HintCode != "" {
		t.Fatalf("turn.spend.runway_low must stay a template binding, got %+v", runway)
	}
}

func TestWorkflowTemplateOverrideWithSameStemPreservesTemplateRendering(t *testing.T) {
	reg := loadTestRegistry(t)
	wf := "custom-wf"
	override, err := anchor.NewWorkflowBinding(anchor.WorkflowInject{
		On:       string(anchor.ComposeDone),
		Selector: anchor.Selector{Surface: "coordinator", Workflow: &wf},
		Effect:   "inform",
		Render:   "coordinator-compose-done", // Same render stem as stock hint binding.
		Tier:     "workflow",
	}, wf, "1.0.0")
	testutil.FailErr(t, "new workflow override", err)
	reg.IndexBinding(override)

	tpl := &stubTemplateEngine{templates: map[string]string{
		"coordinator-compose-done": "Custom workflow v1.0.0 template copy.",
	}}
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(tpl)
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(reg)

	// Emit under the overriding workflow version: must carry template target and render template.
	bus.EmitMatch(t.Context(), "sess-override", anchor.ComposeDone, anchor.Envelope{}, anchor.MatchContext{
		Surface:         "coordinator",
		Workflow:        wf,
		WorkflowVersion: "1.0.0",
	})
	if id := kicks.TakePendingKickIDUnless("sess-override", nil); id != "coordinator-compose-done" {
		t.Fatalf("override kick id = %q", id)
	}
	text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "sess-override", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render override", err)
	if !ok || text != "Custom workflow v1.0.0 template copy." {
		t.Fatalf("override rendered = (%q, %v), want custom template copy", text, ok)
	}
	if len(tpl.calls) != 1 || tpl.calls[0] != "coordinator-compose-done" {
		t.Fatalf("template engine calls = %v, want [coordinator-compose-done]", tpl.calls)
	}
	kicks.AckPendingNudge("sess-override", lease)

	// Emit under another workflow version: falls back to stock OAR hint without calling template engine.
	bus.EmitMatch(t.Context(), "sess-stock", anchor.ComposeDone, anchor.Envelope{}, anchor.MatchContext{
		Surface:         "coordinator",
		Workflow:        wf,
		WorkflowVersion: "2.0.0",
	})
	_ = kicks.TakePendingKickIDUnless("sess-stock", nil)
	stockText, stockLease, stockOK, err := kicks.RenderPendingNudge(t.Context(), "sess-stock", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render stock fallback", err)
	wantStock, _ := reg.HintMessage("WORKFLOW_COMPOSE_SUCCEEDED")
	if !stockOK || stockText != wantStock {
		t.Fatalf("stock fallback = (%q, %v), want %q", stockText, stockOK, wantStock)
	}
	if len(tpl.calls) != 1 {
		t.Fatalf("stock hint must not invoke template engine; calls = %v", tpl.calls)
	}
	kicks.AckPendingNudge("sess-stock", stockLease)
}

func TestUnregisteredHintCodeRendersBareCodeInQueuedKick(t *testing.T) {
	reg := loadTestRegistry(t)
	wf := "missing-hint-wf"
	override, err := anchor.NewWorkflowBinding(anchor.WorkflowInject{
		On:       string(anchor.ComposeDone),
		Selector: anchor.Selector{Surface: "coordinator", Workflow: &wf},
		Effect:   "inform",
		Render:   "coordinator-compose-done",
		HintCode: "UNREGISTERED_WORKFLOW_HINT",
		Tier:     "workflow",
	}, wf, "1.0.0")
	testutil.FailErr(t, "new workflow hint binding", err)
	reg.IndexBinding(override)

	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(reg)

	bus.EmitMatch(t.Context(), "sess-bare", anchor.ComposeDone, anchor.Envelope{}, anchor.MatchContext{
		Surface:         "coordinator",
		Workflow:        wf,
		WorkflowVersion: "1.0.0",
	})
	_ = kicks.TakePendingKickIDUnless("sess-bare", nil)
	text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "sess-bare", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render unregistered hint", err)
	if !ok || text != "UNREGISTERED_WORKFLOW_HINT" {
		t.Fatalf("unregistered hint = (%q, %v), want bare code", text, ok)
	}
	kicks.AckPendingNudge("sess-bare", lease)
}

func TestFeedbackPendingSuppressionPreservedBesideHint(t *testing.T) {
	loadTestRegistry(t)
	codes := []string{"WORKFLOW_FEEDBACK_PENDING", "OTHER_CODE"}
	filtered := anchor.FilterSuppressedHintCodes(codes, api.CoordinatorRunContext{}, anchor.InformRender(anchor.FeedbackPending))
	if len(filtered) != 1 || filtered[0] != "OTHER_CODE" {
		t.Fatalf("filtered = %v, want [OTHER_CODE]", filtered)
	}
}

func TestNonInformHintCodeRejected(t *testing.T) {
	wf := "wf"
	_, err := anchor.NewWorkflowBinding(anchor.WorkflowInject{
		On:       string(anchor.ComposeDone),
		Selector: anchor.Selector{Surface: "coordinator", Workflow: &wf},
		Effect:   "block",
		HintCode: "WORKFLOW_COMPOSE_SUCCEEDED",
		Tier:     "workflow",
	}, wf, "1.0.0")
	if err == nil {
		t.Fatal("expected error for hint_code on non-inform binding")
	}
}

func TestRegistryHintLookupUsesAttachedRuleSet(t *testing.T) {
	schemaDir := configlayout.SchemasDir(configlayout.FindModuleRoot())
	testutil.FailErr(t, "install declaration", oar.InstallEngineDeclarationBundled(schemaDir))
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "new loader", err)
	h, err := loader.LoadHintJSON(json.RawMessage(`{"spec_version":"1.0","id":"CUSTOM_HINT","kind":"hint","message":"Custom hint text."}`))
	testutil.FailErr(t, "load hint", err)
	if h == nil || h.ID != "CUSTOM_HINT" {
		t.Fatalf("loaded hint = %+v", h)
	}
	_ = oarcore.CheckRuleSet(nil, []*oarcore.Hint{h})
}
