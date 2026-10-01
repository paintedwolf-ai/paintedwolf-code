package workflowvalidate_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
)

// failClosedClass is one host stuckness class that ValidateCatalog (or an
// explicit waiver) must cover. Adding a new fail-closed authoring constraint
// without a probe or waiver fails this inventory.
type failClosedClass struct {
	Name   string
	Code   workflowdiag.Code
	YAML   string // broken overlay/path fixture; empty if Probe or Waiver set
	Waiver string // non-empty = not covered by ValidateCatalog
	// Probe runs instead of YAML when the class is enforced via a shared helper
	// that ValidateCatalog already calls (no shipped surface can violate it).
	Probe func(t *testing.T)
}

func TestFailClosedCoverageInventory(t *testing.T) {
	root := configlayout.FindModuleRoot()
	classes := append(failClosedLeaveabilityClasses(), failClosedClosureClasses()...)
	for _, c := range classes {
		t.Run(c.Name, func(t *testing.T) {
			runFailClosedClass(t, root, c)
		})
	}
}

func runFailClosedClass(t *testing.T, root string, c failClosedClass) {
	t.Helper()
	if c.Waiver != "" {
		t.Logf("waiver: %s", c.Waiver)
		return
	}
	if c.Probe != nil {
		c.Probe(t)
		return
	}
	if c.YAML == "" {
		t.Fatal("probe YAML or Probe required unless Waiver set")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(c.YAML), 0o644))
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{path},
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	for _, d := range diags {
		if d.Code == string(c.Code) {
			return
		}
	}
	for _, d := range diags {
		t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
	}
	t.Fatalf("expected code %s for class %s", c.Code, c.Name)
}

func failClosedLeaveabilityClasses() []failClosedClass {
	out := failClosedSurfaceLeaveabilityClasses()
	out = append(out, failClosedRequiredToolClasses()...)
	return append(out, failClosedReviewBindingClasses()...)
}

func failClosedSurfaceLeaveabilityClasses() []failClosedClass {
	return []failClosedClass{
		{
			Name: "terminal_stamp",
			Code: workflowdiag.MustCode("terminal_requires_orchestration_complete"),
			YAML: `id: inv-terminal
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [hitl_consulted:only]
    terminal: true
`,
		},
		{
			Name: "prompted_phase_needs_coordinator_surface",
			Code: workflowdiag.MustCode("missing_coordinator_surface"),
			YAML: `id: inv-missing-surface
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: report
    activity_label: Synthesizing
    on_enter:
      prompt_coordinator: true
    complete_when: gates_satisfied
    gates: [topology_report_delivered]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "report_phase_needs_report_exit_surface",
			Code: workflowdiag.MustCode("report_phase_exit_mismatch"),
			YAML: `id: inv-report-exit
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: report
    activity_label: Synthesizing the findings
    coordinator_surface: survey_execute
    surface_template: agents/coordinator-surface-vet.md
    on_enter:
      prompt_coordinator: true
    complete_when: gates_satisfied
    gates: [topology_report_delivered]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
	}
}

// failClosedRequiredToolClasses covers phase policies that oblige a phase to
// declare a tool: advance, review verdict, ask-user wait, progress update.
func failClosedRequiredToolClasses() []failClosedClass {
	return []failClosedClass{
		{
			Name: "coordinator_advance_needs_workflow_advance",
			Code: workflowdiag.MustCode("missing_tool_for_advance_policy"),
			YAML: `id: inv-advance
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: stub
    activity_label: Drafting the stub
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    complete_when: gates_satisfied
    gates: [human_approval]
    advance: { when_gate_met: coordinator }
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "review_loop_needs_submit_verdict",
			Code: workflowdiag.MustCode("missing_review_verdict_tool"),
			YAML: `id: inv-review-verdict
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: judge
    activity_label: Judge
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    review_loop:
      evidence_key: inv_judge
      iteration_cap: 2
      verdict_schema:
        verdict: APPROVED|NEEDS_REVISION
        bullets: string
    complete_when: gates_satisfied
    gates: ["evidence_passed:inv_judge"]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "hitl_facts_need_ask_user_wait",
			Code: workflowdiag.MustCode("missing_hitl_tools"),
			YAML: `id: inv-hitl
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: intake
    activity_label: Understanding the request
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    complete_when: gates_satisfied
    gates: [hitl_consulted:intake]
    advance: { when_gate_met: coordinator }
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "progress_gated_needs_update_progress",
			Code: workflowdiag.MustCode("surface_missing_update_progress"),
			Probe: func(t *testing.T) {
				// Emitter: checkSurfaceProgress. Predicate authority:
				// progress.SurfaceMissingUpdateProgress / IsProgressGatedTool.
				if !progress.SurfaceMissingUpdateProgress([]string{"edit", "read"}) {
					t.Fatal("predicate must flag gated tools without update_progress")
				}
				diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
					ConfigRoot: configlayout.FindModuleRoot(),
					Mode:       workflowvalidate.ModeBundled,
				})
				testutil.FailErr(t, "ValidateCatalog bundled", err)
				for _, d := range diags {
					if d.Code == string(workflowdiag.MustCode("surface_missing_update_progress")) {
						t.Fatalf("bundled surfaces violate progress SSOT: %v", d)
					}
				}
			},
		},
	}
}
func failClosedReviewBindingClasses() []failClosedClass {
	return []failClosedClass{
		{
			Name: "review_loop_needs_spawnable_required_agent",
			Code: workflowdiag.MustCode("missing_review_spawn_agent"),
			YAML: `id: inv-review-spawn
version: 1.0.0
agents:
  - { id: implementer, tools: profile }
  - { id: skeptic, tools: profile }
phases:
  - id: judge
    activity_label: Judge
    coordinator_surface: implement_dispatch
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    review_loop:
      evidence_key: inv_judge
      iteration_cap: 2
      required_agents: [skeptic]
      verdict_schema:
        verdict: APPROVED|NEEDS_REVISION
        bullets: string
    complete_when: gates_satisfied
    gates: ["evidence_passed:inv_judge"]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "review_loop_required_agent_declared",
			Code: workflowdiag.MustCode("review_agent_not_declared"),
			YAML: `id: inv-review-undeclared
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: judge
    activity_label: Judge
    coordinator_surface: plan_review
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    review_loop:
      evidence_key: inv_judge
      iteration_cap: 2
      required_agents: [skeptic]
      verdict_schema:
        verdict: APPROVED|NEEDS_REVISION
        bullets: string
    complete_when: gates_satisfied
    gates: ["evidence_passed:inv_judge"]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "review_loop_if_spawnable_declared",
			Code: workflowdiag.MustCode("review_agent_not_declared"),
			YAML: `id: inv-review-spawnable-undeclared
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: judge
    activity_label: Judge
    coordinator_surface: plan_review
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    review_loop:
      evidence_key: inv_judge
      iteration_cap: 1
      if_spawnable: [web-researcher]
      verdict_schema:
        verdict: CHALLENGED
    complete_when: gates_satisfied
    gates: ["evidence_passed:inv_judge"]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "review_loop_agent_role_conflict",
			Code: workflowdiag.MustCode("review_agent_role_conflict"),
			YAML: `id: inv-review-role-conflict
version: 1.0.0
agents:
  - { id: implementer, tools: profile }
  - { id: skeptic, tools: profile }
phases:
  - id: judge
    activity_label: Judge
    coordinator_surface: plan_review
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [plan-stub]
    review_loop:
      evidence_key: inv_judge
      iteration_cap: 1
      required_agents: [skeptic]
      if_spawnable: [skeptic]
      verdict_schema:
        verdict: CHALLENGED
    complete_when: gates_satisfied
    gates: ["evidence_passed:inv_judge"]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
	}
}

func failClosedOverlayClasses() []failClosedClass {
	return []failClosedClass{
		{
			Name: "overlay_gate_kit",
			Code: workflowdiag.MustCode("domain_leaf_on_overlay"),
			YAML: `id: inv-domain
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [plan_stub_valid]
    terminal: true
`,
		},
		{
			Name: "overlay_explicit_surface",
			Code: workflowdiag.MustCode("overlay_requires_explicit_surface"),
			YAML: `id: inv-surface
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: only
    activity_label: Running
    complete_when: orchestration_complete
    terminal: true
`,
		},
		{
			Name: "overlay_attach_session_create",
			Code: workflowdiag.MustCode("attach_session_create_on_overlay"),
			YAML: `id: inv-attach
version: 1.0.0
attach:
  policy: session_create
agents: [{ id: implementer, tools: profile }]
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: orchestration_complete
    terminal: true
`,
		},
	}
}

func failClosedClosureClasses() []failClosedClass {
	return append(failClosedOverlayClasses(), []failClosedClass{
		{
			Name: "unknown_agent",
			Code: workflowdiag.MustCode("unknown_agent"),
			YAML: `id: inv-agent
version: 1.0.0
agents: [{ id: totally-fake-agent, tools: profile }]
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: orchestration_complete
    terminal: true
`,
		},
		{
			Name: "kicks_exist",
			Code: workflowdiag.MustCode("missing_kick"),
			YAML: `id: inv-kick
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: work
    activity_label: Work
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [human_approval]
    on_reenter:
      inject_kick: this-kick-does-not-exist
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "mode_refs_resolve",
			Code: workflowdiag.MustCode("missing_mode_ref"),
			YAML: `id: inv-mode
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [this-mode-ref-does-not-exist]
    complete_when: orchestration_complete
    terminal: true
`,
		},
		{
			Name: "topology_bind_in_topology",
			Code: workflowdiag.MustCode("topology_bind_unknown"),
			YAML: `id: inv-topo
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
topology: default-pipeline
phases:
  - id: research
    activity_label: Researching
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    bind_topology_stage: this-stage-does-not-exist
    complete_when: gates_satisfied
    gates: [human_approval]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			Name: "choice_transition_reachability",
			Code: workflowdiag.MustCode("phase_unreachable"),
			// ValidatePhaseReachability walks next + transitions[].to.
			// A phase with neither inbound edge must emit phase_unreachable (pre-finalize).
			YAML: `id: inv-orphan-phase
version: 1.0.0
agents: [{ id: implementer, tools: profile }]
phases:
  - id: start
    activity_label: Start
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [human_approval]
    next: done
  - id: orphan
    activity_label: Orphan
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [human_approval]
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`,
		},
	}...)
}
