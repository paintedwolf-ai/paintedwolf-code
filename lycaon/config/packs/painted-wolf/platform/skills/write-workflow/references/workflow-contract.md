# Project workflow contract

Maintained source of truth: `docs/workflows.md`. Do not treat bundled workflow prompt bodies as a public template.

## Path

```
{workspace}/${overlay_dir}/workflows/<workflow_id>/workflow.yaml
```

A bare `${overlay_dir}/workflows/<name>.yaml` does not load. Extension-pack workflow units live at `workflows/<id>/workflow.yaml` inside a pack (unit id `workflows/<id>`) and may use bundled domain leaves; project overlays may not.

## Request contract

Every effective workflow declares how it obtains its request:

```yaml
request:
  cadence: once # optional; once is the default
  question: What should this workflow focus on?
  default: Review the project broadly. # optional
```

Explicit invocation text wins. With no text, the host uses `default` when present; otherwise it immediately opens `question` as durable user input. Use `cadence: each_turn` for an ambient conversational workflow that accepts a new request on every turn. Session attachment is activation only and does not itself open the question. Invoked children inherit the parent request.

## Generic gate kit (project overlays)

Static leaves: `gates_satisfied`, `orchestration_complete`, `child_run_complete`, `child_run_failed`, `choice_transition_required`, `human_approval`.

Parameterized prefixes (non-empty suffix required): `var_equals:`, `var_truthy:`, `user_decision_received:`, `user_feedback_received:`, `user_decision:`, `hitl_consulted:`, `evidence_passed:`, `phase_is:`, `phase_skipped:`, `gate_passed:`, `obligation_settled:`.

`PublicGateKit` in `internal/workflow/gate_kit.go` is the enforcing source; `schemas/workflow_vocabulary.json` carries the generated per-identifier layer and domain.

`complete_when` accepts `gates_satisfied` (which requires a non-empty `gates: […]` — the one form the validator refuses when the list is empty), `gate_satisfied:<leaf>`, a bare leaf from the kit above, or a boolean expression over those identifiers. A terminal phase must use `complete_when: orchestration_complete`. Terminal entry is an atomic host completion sink: do not bind coordinator work, feedback, obligations, or invocation to it.

## Approval and child workflows

Every `human_approval:` phase is Blueprint-backed; the manifest must declare `blueprint:` and approval binds the reviewed bytes. Approval readiness requires materialized Blueprint content, not the generated scaffold. Keep document frontmatter at `status: draft`; `approved`, `implementing`, and `done` are host-set lifecycle states.

A phase that authors or revises the Blueprint declares `blueprint_write: true`. Every Blueprint-consuming workflow needs at least one declared writer phase, and each writer's resolved coordinator surface must include a Blueprint-capable write tool (`write`, `edit`, or `replace_lines`). A verdict-producing decision workflow writes its durable selection report before submitting the verdict or exposing approval.

Every `invoke_workflow:` block must declare `blueprint: inherit | own | none`. `inherit` requires an approved parent Blueprint, `own` creates one for the child, and `none` requires a documentless child. Children cannot invoke another workflow and must have a terminal child path. Use `child_complete_when:` plus optional `child_gates:` when the child needs a different completion proof, and `child_next:` on a looping phase when the child needs a finite exit while the root run keeps its ordinary contract.

An approval that enters an invocation phase wakes the current active leaf after the child start commits. Do not add a second parent wake or model-authored handoff; terminal root approval emits no coordinator wake.

Topology workers produce findings and evidence. The coordinator writes protected workflow-control artifacts such as Blueprints; pipeline stages do not promote worker overlays into those paths.

## Forbidden on project overlays

Bundled domain leaves — `plan_stub_valid`, `research_satisfied`, `topology_stage_complete`, `delegation_closeout_complete`, `closeout_gates_passed`, `delivery_gates_passed`, `recon_or_board_ready`, `worker_cycle_ready`, `topology_report_delivered`, `fanout_planned`, `parallel_stages_complete`, `implement_workflow_ready`, `options_selection_valid` — diagnostic `domain_leaf_on_overlay`. Anything outside the kit above is refused, so treat the kit as the allowance rather than this list as the denial.

A domain leaf is refused wherever it appears — in `gates:`, after `gate_satisfied:`, or as a bare `complete_when:`. Also forbidden: `agents[]` entries without `tools: all | profile`. Unknown manifest fields fail load.

## Leaveability (host facts)

| Host fact | Required tools on the resolved surface |
|---|---|
| Advance policy is coordinator | `workflow_advance` |
| Phase binds HITL | `ask_user` + `wait` (+ `workflow_advance` when advance is coordinator-controlled) |
| `review_loop:` phase block | `submit_verdict` |
| Surface lists a progress-gated tool | `update_progress` |

A `review_loop` whose `verdict_schema` has a `claims` member declares `claim_statuses`, mapping each status word to `held`, `failed`, or `open`; add `brief_label` to list the review as a check in the report. A report workflow may declare `controls.report.brief`: the rating question, the facts each rated finding answers, and the levels those answers decide, the last level without conditions.

Declare each phase's `coordinator_surface` and `surface_template` explicitly.
The surface catalog supplies its required base mode. Set `mode_refs` only as an
intentional phase override; project overlay phases must still provide the
explicit binding fields required by overlay validation.

## Validator commands

```bash
./task validate:workflows
go run ./cmd/lycaon workflow validate
go run ./cmd/lycaon workflow validate --json PATH…
```

Diagnostics expose `field` / `code` / `message` / `replacement`. Branch on `code`. Green means the recipe loads and phases are leaveable — not that the model will behave well in production.
