package composition_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

func testComposer(t *testing.T) *workflowcomposition.Composer {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	return &workflowcomposition.Composer{
		SessionStore: workflowdrafts.NewMemory(),
		Registry:     reg,
		Agents:       agents,
		Policy:       testComposePolicy(t),
	}
}

func TestComposeValidatorAllowsPlanChildShortcut(t *testing.T) {
	c := testComposer(t)
	manifest := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: gates_satisfied
    gates:
      - delegation_closeout_complete
      - evidence_passed:verify
`
	result, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{
		SessionID:      "sess-1",
		ManifestYAML:   []byte(manifest),
		SessionPosture: "spec",
		CreatedBy:      "coordinator",
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if result.Summary.Scope != "session" {
		t.Fatalf("scope = %q", result.Summary.Scope)
	}
	if result.EffectiveSummary.Extends != "plan@1.0.0" {
		t.Fatalf("extends = %q", result.EffectiveSummary.Extends)
	}
	if len(result.EffectiveSummary.PhasesRemovedFromParent) == 0 {
		t.Fatal("expected removed parent phases")
	}
	if strings.TrimSpace(result.EffectiveSummary.CoordinatorBrief) == "" {
		t.Fatal("expected coordinator_brief")
	}
}

func TestComposeValidatorRejectsTrigger(t *testing.T) {
	c := testComposer(t)
	manifest := `id: bad
version: 1.0.0
trigger: /nope
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	_, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{SessionID: "s", ManifestYAML: []byte(manifest)})
	var vf *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	if vf.Errors[0].Code != "session_workflows_no_trigger" {
		t.Fatalf("code = %q", vf.Errors[0].Code)
	}
}

func TestComposeValidatorRejectsTooManyPhases(t *testing.T) {
	c := testComposer(t)
	manifest := `id: wide
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    complete_when: plan_stub_valid
    next: b
  - id: b
    activity_label: Test phase
    complete_when: plan_stub_valid
    next: c
  - id: c
    activity_label: Test phase
    complete_when: plan_stub_valid
    next: d
  - id: d
    activity_label: Test phase
    complete_when: plan_stub_valid
    next: e
  - id: e
    activity_label: Test phase
    complete_when: plan_stub_valid
    next: build
  - id: build
    activity_label: Test phase
    complete_when: delegation_closeout_complete
`
	_, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{SessionID: "s", ManifestYAML: []byte(manifest)})
	var vf *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	found := false
	for _, e := range vf.Errors {
		if e.Code == "max_phases_exceeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %+v", vf.Errors)
	}
}

func TestComposeValidatorRejectsUnknownAgent(t *testing.T) {
	c := testComposer(t)
	manifest := `id: bad-agents
version: 1.0.0
extends: plan@1.0.0
agents:
  - { id: not-a-real-agent, tools: profile }
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    complete_when: delegation_closeout_complete
`
	_, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{SessionID: "s", ManifestYAML: []byte(manifest)})
	var vf *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	if vf.Errors[0].Code != "unknown_agent" {
		t.Fatalf("code = %q", vf.Errors[0].Code)
	}
}

func TestPrimitiveValidatorRequiresBlueprintForHumanApproval(t *testing.T) {
	errs := workflowvalidation.ValidatePrimitiveManifest(workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{
		ID:            "approve",
		HumanApproval: &workflowdef.HumanApprovalConfig{},
	}}})
	if len(errs) != 1 || errs[0].Code != "human_approval_blueprint_required" {
		t.Fatalf("errors = %+v", errs)
	}
}

func TestComposeValidatorRejectsForbiddenPredicate(t *testing.T) {
	c := testComposer(t)
	manifest := `id: bad-pred
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    complete_when: stage_plan_complete
    next: build
  - id: build
    activity_label: Test phase
    complete_when: delegation_closeout_complete
`
	_, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{SessionID: "s", ManifestYAML: []byte(manifest)})
	var vf *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	if vf.Errors[0].Code != "forbidden_predicate" {
		t.Fatalf("code = %q", vf.Errors[0].Code)
	}
}

func TestComposeDryRunDoesNotPersist(t *testing.T) {
	c := testComposer(t)
	store := c.SessionStore.(*workflowdrafts.Memory)
	manifest := `id: dry
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	_, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{
		SessionID: "sess-dry", ManifestYAML: []byte(manifest), DryRun: true,
	})
	testutil.FailErr(t, "c.Compose failed", err)
	rows, err := store.ListBySession(context.Background(), "sess-dry")
	testutil.FailErr(t, "store.ListBySession failed", err)
	if len(rows) != 0 {
		t.Fatalf("rows = %d want 0", len(rows))
	}
}
