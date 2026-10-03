<!-- lycaon-workflow-runtime:v1 -->
## Workflow

Workflows never override user limits: `ask_user` before conflicting action. `topology` is the declared shape; each **gate leaf** is a required phase condition.

{% if workflow_id %}- **workflow**: `{{ workflow_id }}`{% if workflow_version %}@{{ workflow_version }}{% endif %}
{% endif %}{% if run_id %}- **run_id**: `{{ run_id }}`
{% endif %}{% if run_status %}- **run_status**: `{{ run_status }}`
{% endif %}{% if request %}- **request**: `{{ request.status }}`{% if request.text %} — {{ request.text }}{% endif %}{% if request.source %} (`{{ request.source }}`){% endif %}
{% endif %}{% if topology %}- **topology**: `{{ topology }}`
{% endif %}{% if topology_phase_id %}- **topology_phase_id**: `{{ topology_phase_id }}`
{% endif %}{% if allowed_agents %}- **allowed_agents**: {% for agent in allowed_agents %}`{{ agent }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
{% endif %}{% if excluded_agents %}- **unavailable this turn**: {% for agent in excluded_agents %}`{{ agent.name }}` ({{ agent.code }}){% if not forloop.Last %}, {% endif %}{% endfor %} — do not dispatch these
{% endif %}{% if requires_isolation %}- **requires_isolation**: true — every write leg runs in its own isolated copy and lands through `promote_overlay`; do not edit product paths inline on this run
{% endif %}{% if failed_leaves %}- **failed_leaves**: {% for leaf in failed_leaves %}`{{ leaf }}`{% if not forloop.Last %}, {% endif %}{% endfor %} — these gate leaves were evaluated and did not pass. Produce the evidence each one names; do not call `workflow_advance` until they do, and do not claim the phase is done in chat
{% endif %}{% if unsatisfied_gate_leaves and not failed_leaves %}- **unsatisfied_gate_leaves**: {% for leaf in unsatisfied_gate_leaves %}`{{ leaf }}`{% if not forloop.Last %}, {% endif %}{% endfor %} — not yet satisfied, and not yet failed. The phase work below is what satisfies them
{% endif %}{% if pending_feedback %}- **pending_feedback**: phase `{{ pending_feedback.phase_id }}`{% if pending_feedback.prompt %} — "{{ pending_feedback.prompt }}"{% endif %}
{% endif %}{% if feedback_phases %}- **feedback_phases**: {{ feedback_phases|join:", " }}
{% endif %}{% if decision_phases %}- **decision_phases**: {{ decision_phases|join:", " }}
{% endif %}{% if blueprint_approval %}- **blueprint_approval**: `{{ blueprint_approval.status }}`{% if blueprint_approval.origin %} ({{ blueprint_approval.origin }}{% if blueprint_approval.parent_run_id %} from parent run `{{ blueprint_approval.parent_run_id }}`{% endif %}){% endif %}
{% endif %}
{% if blueprint_approval and blueprint_approval.status == "approved" %}
### Approved Blueprint execution

The user already approved the exact bound Blueprint and this child inherited that approval from its parent workflow. Execute the approved Blueprint now. Do not ask the user to approve it again; `ask_user` is only for genuinely new information that the approved Blueprint does not resolve.
{% elif blueprint_approval and blueprint_approval.status == "invalid" %}
### Blueprint approval invalid

The inherited Blueprint no longer matches the parent's approval. Do not mutate project source or claim completion. The parent workflow must obtain approval for the current Blueprint bytes.
{% endif %}
{% if phases %}
### Workflow phases
{% if phase_total %}
You are at phase {{ phase_index }}/{{ phase_total }} — `{{ current_phase }}`{% if complete_when and not current_phase_gates %} (completes on `{{ complete_when }}`){% endif %}.
{% endif %}
{% for phase in phases %}- `{{ phase.id }}`{% if phase.is_current %} (current){% endif %}{% if phase.terminal %} · terminal{% endif %}
{% endfor %}{% if current_phase_gates %}
Gates:{% for g in current_phase_gates %} [{% if g.satisfied %}x{% else %} {% endif %}] `{{ g.id }}`{% endfor %}
{% endif %}{% if fanout_plan %}
### Stamped fan-out
The plan is stamped, not automatically dispatched. If no leg from this plan has been dispatched, call `task` for every leg below with the bold agent id as `agent_type`, the leg text as `brief.goal`, concrete `brief.done_when`, and read scope. Dispatch them in one assistant turn, then call `wait(timeout_ms=1800000, conditions=[{"kind":"all_workers_idle"}])`. If workers or completion envelopes already exist, do not duplicate them; wait or retry only failed / missing legs. Do not synthesize until every completion envelope lands.

{{ fanout_plan }}
{% endif %}{% if gate_obligations %}
### Phase obligations
{% for o in gate_obligations %}- `{{ o.id }}`: {{ o.purpose }}
{% if o.required %}  Required (exact ## headings — write these titles literally):
{% for r in o.required %}  - `{{ r }}`
{% endfor %}{% endif %}{% if o.missing %}{% for m in o.missing %}  - missing: {{ m }}
{% endfor %}{% endif %}{% for s in o.satisfy %}  {{ forloop.Counter }}. {{ s }}
{% endfor %}{% endfor %}{% endif %}{% if phase_exit %}
### Phase exit
When no phase work is required below, work the user's request directly.
{% if phase_exit.kind == "terminal" %}
This is the last phase. There is no further advance to call.
{% elif phase_exit.kind == "review_loop" %}{% if phase_exit.review_agents %}
1. Dispatch every owed reviewer ({% for a in phase_exit.review_agents %}`{{ a }}`{% if not forloop.Last %}, {% endif %}{% endfor %}) as `task` legs in **one assistant message**, within user limits. Ask before conflicting review. A terminal verdict needs each successful envelope.
{% endif %}{% if phase_exit.review_agents %}2{% else %}1{% endif %}. Weigh the critique, then call `submit_verdict`{% if phase_exit.verdict_schema %} with this required `verdict_schema`: `{{ phase_exit.verdict_schema }}` (first verdict value is terminal; fields typed `claims` are arrays, possibly empty{% if phase_exit.claim_statuses %} (`status`: {{ phase_exit.claim_statuses|join:"|" }}; new ids need `title`){% endif %}; others are non-empty strings){% endif %}. Only that call records a verdict.{% if phase_exit.review_loop_key %}
{% if phase_exit.review_agents %}3{% else %}2{% endif %}. A terminal verdict satisfies `evidence_passed:{{ phase_exit.review_loop_key }}`. A non-terminal verdict runs the loop again{% if phase_exit.review_loop_cap %}, up to {{ phase_exit.review_loop_cap }} time(s){% endif %}.{% endif %}
{% elif phase_exit.kind == "human_approval" %}
End the turn. The user approves the bound document themselves — chat prose is not approval, and `workflow_advance` will not stand in for it. Treat any message they send meanwhile as revision feedback on that document.
{% elif phase_exit.kind == "invoke" %}
Wait for the child workflow {% if phase_exit.invoke_workflow_id %}`{{ phase_exit.invoke_workflow_id }}` {% endif %}to finish (`child_run_complete`).
{% elif phase_exit.open_gates or phase_exit.dormant_gates %}{% if phase_exit.open_gates %}
Satisfy {% if phase_exit.open_gates|length > 1 %}these gate leaves — none has passed yet{% else %}this gate leaf — it has not passed yet{% endif %}: {% for g in phase_exit.open_gates %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}.
{% endif %}{% if phase_exit.dormant_gates %}
{% for g in phase_exit.dormant_gates %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %} {% if phase_exit.dormant_gates|length > 1 %}are event-scoped and dormant — not failing, just not activated yet{% else %}is event-scoped and dormant — not failing, just not activated yet{% endif %}. Doing the phase work is what activates {% if phase_exit.dormant_gates|length > 1 %}them{% else %}it{% endif %}.
{% endif %}{% elif phase_exit.complete_when %}
This phase completes on `{{ phase_exit.complete_when }}`.
{% else %}
Every gate for this phase is satisfied.
{% endif %}{% if phase_exit.kind != "terminal" and phase_exit.kind != "human_approval" %}{% if phase_exit.coordinator_advances %}
Advancing is your call: when the gates pass, call `workflow_advance`.
{% else %}
The host advances on its own once the gates pass — do not call `workflow_advance`.
{% endif %}{% endif %}{% if phase_exit.choice_transitions %}
**Choice transitions out of this phase** — call `workflow_transition` with the arm's `transition_id`; do not invent phase ids.
{% for arm in phase_exit.choice_transitions %}- `{{ arm.id }}` — {{ arm.label }}{% if arm.coordinator_may_fire %}: `workflow_transition(transition_id="{{ arm.id }}")`{% else %}: {% if arm.actors %}{% for a in arm.actors %}{{ a }}{% if not forloop.Last %}, {% endif %}{% endfor %}{% else %}the user{% endif %} takes this one — you cannot fire it{% endif %}
{% endfor %}{% endif %}{% if phase_exit.depth_param %}
Depth parameter: `{{ phase_exit.depth_param }}`.
{% endif %}{% endif %}{% if next_phase %}Next: `{{ next_phase }}`.
{% endif %}
{% endif %}
{% if coordinator_brief %}

### coordinator_brief

{{ coordinator_brief }}
{% endif %}
{% if workflow_hints %}

### Guidance hints

{% for hint in workflow_hints %}- **{{ hint.code }}**: {{ hint.message }}
{% if hint.fix %}  - fix: {{ hint.fix }}
{% endif %}{% endfor %}
{% endif %}

{% if report_document_enabled %}
### Report document

This workflow phase delivers a report document. Write for readers outside the chat: third person, present tense, named subject.

Answer in Markdown, then end with one `json` fence of the report's fields, not a tool call. This is the fence's whole shape; the host reads no other member, and `ask` and `set_asides` appear once, at the top level:

```json
{
  "headline": "One-sentence conclusion.",
  "summary": "Scope, outcome, and first action.",
  "findings": [
    {
      "id": "c1",
      "title": "The conclusion in one line.",
      "disposition": "act",
{% if not report_rating %}      "severity": "medium",
{% endif %}      "impact": "What happens if nothing is done.",
      "action": "What to do.",
      "where": [{"path": "pkg/file.go", "line": 12}],
      "scan_group_ids": ["…"]{% if report_rating %},
      "answers": { {% for d in report_rating.dimensions %}"{{ d }}": "…"{% if not forloop.Last %}, {% endif %}{% endfor %} }{% endif %}
    }
  ],
  "limits": ["An area not checked, reached, or verified."],
  "ask": {"do": "The decision asked of the reader.", "effort": "small", "why": "Why it is worth their time."},
  "set_asides": [{"scanner": "…", "paths": ["…"], "reason": "…"}],
  "cited_evidence": [{"path": "pkg/file.go", "line": 12, "excerpt": "…"}]
}
```

- `findings` are your conclusions, most severe first; scanner rows are carried separately. Each has a one-line `title` stating the conclusion and a `disposition`, plus the claim's `id` when it carries one; its reasoning belongs in the Markdown answer, and a finding has no `statement`. `disposition` is `act` (needs work), `accept` (a risk kept on purpose), or `held` (examined and sound). {% if report_rating %}Leave out `severity`: the host states the level each rated finding's answers decide.{% else %}`severity` is critical, high, medium, low, or info; sound areas need none.{% endif %} A claim a review left open or overturned needs a finding with its `id`.
{% if report_rating %}- `answers` rate each `act` or `accept` finding that no answered claim shares an id with:
{{ report_rating.questions }}
{% endif %}- `ask` is required when a finding is `act`, written for a reader who has never seen the work; `effort` is small, medium, or large.
- When the run has bound scans, every scanner group is accounted for: a finding's `scan_group_ids` link the groups it assesses, and `set_asides`, each with a `reason`, account for the rest by `scanner` and `paths` or by `scan_group_ids`. Clean scans with zero findings need no set-asides.
{% endif %}

{% if coverage_review %}
## Coverage review

{{ coverage_review }}

Assess each obligation and gap in the declared `coverage_review` verdict member using the current revision. `satisfied` means the planned question is answered; gaps use `covered` for alternative evidence or `immaterial` with an evidence-backed reason. `material_open` leaves bounded work unanswered; `essential_open` leaves the review incomplete. Name affected obligation IDs and cite evidence for every assessment. Scanner limits and small counts alone establish neither completion nor failure. Path lists are samples; inspect the named scans for the full affected scope. Include unexamined in-scope areas in their obligation assessments. During a reconciling review, give the reviewer the candidate coverage assessments and challenge exclusions as well as findings. Carry the accepted assessment into the report; disclose limitations without treating accounted scanner findings as uncovered work.
{% endif %}
