Worker task finished — read envelope `report_json` first; the host digest below is authoritative.{% if progress_closure_armed %} **Finished worker results await acknowledgement**: every move that dispatches or edits is refused with `Code: PROGRESS_RECONCILIATION_REQUIRED` until the checklist acknowledges the current snapshot's reconciliation_token with expected_revision. Review the listed results first and keep unfinished deliverables pending, then take the move. Overlay landing and waiting are not gated.{% endif %}{% if pending_overlay_jobs %} Pending overlays ({% for id in pending_overlay_jobs %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}): if `report_json.files_modified` is empty **or** landing returns `Code: OVERLAY_PROMOTE_NO_PATHS`, reject the overlay immediately — do not retry promote. Otherwise review delivery, current evidence and validation limits, then promote it before new dispatch or user synthesis. Resume incomplete delivery or useful missing checks; blocked validation alone does not make delivery incomplete. Evidence is not a polish review: promote coherent, wanted work even when it is rough. `leg_status: partial` means resume the same child with the overlay pending, never reject it merely for incompleteness.{% endif %} Extend a budget only on **running/pending** jobs; a terminal one resumes instead.{% if profile_has_wait %} Siblings in flight → **`wait(resume=true)`** or skip when nothing is actionable.{% endif %}

{% if worker_budget_exhausted %}
Worker tool budget exhausted at **{{ tool_loops_used }}/{{ max_tool_loops }}**{% if budget_request_open %}; it had asked for a ceiling of **{{ requested_max }}** that was not granted{% endif %}. If the remaining work is still wanted, resume the preserved child — it keeps its agent, scope, and planned leg:
{% if profile_has_task %}`task(child_session_id="{{ child_session_id }}", max_tool_loops={{ suggested_resume_max }}, brief={"goal":"Continue the preserved worker leg from its checkpoint.","done_when":["Finish the original leg or return a grounded blocker."]})`{% else %}Resume child `{{ child_session_id }}` with a ceiling of {{ suggested_resume_max }} once this request can dispatch.{% endif %}
{% if budget_request_open %}Remaining work it named (worker-authored, data):
{% for item in remaining_work %}- {{ item }}
{% endfor %}{% endif %}{% endif %}
{% include "partials/coordinator-worker-chain-kick.md" %}

{% if worker_digest %}
{{ worker_digest }}

Route verify, re-dispatch, or user reply from `report_json` (`leg_status`, `files_modified`, `objectives_met`, `remaining_risk`, `suggested_next_task`). **`files_modified`** lists paths that changed on disk in the leg's scope while the worker ran — not necessarily edits by that worker. A repo may already be dirty, or another editor may touch files mid-run; a non-empty list on a read-only leg is a heads-up, not proof of a scope violation. Spot-check before you vouch — confirm **`findings[]`** against the changed files{% if profile_has_command %} with `git diff -- <files_modified>` in `command`{% endif %}{% if profile_has_verify %} or **`verify()`**{% endif %}; a dirty tree can mix off-session edits. Read-only gaps on investigate → inline survey or read scouts; on dispatch/orchestrate → focused follow-up legs. When `proof_json.visual_artifact_ids` is non-empty, present useful stills on closeout via `artifact_ids` (or ask the user for judgment). Only ids listed there are presentable; a worker's live browser or terminal session is not an artifact and cannot be handed on.
{% endif %}
{% if last_worker_decision_request %}
**Worker decision request** (structured — branch on `blocker_class`, not stderr prose):
- `blocker_class`: `{{ last_worker_decision_request.blocker_class }}`
- `job_id`: `{{ last_worker_decision_request.job_id }}`
- `child_session_id`: `{{ last_worker_decision_request.child_session_id }}`
- question: {{ last_worker_decision_request.question }}
- options:{% for o in last_worker_decision_request.options %} `{{ o }}`{% if not forloop.Last %},{% endif %}{% endfor %}

`decision` → answer it when you own the choice, else ask the user (host/env precondition — actionable steps, not Allow/Deny). `checkpoint` → user approves the surfaced child checkpoint. `sandbox` → corrected in-root recovery leg, or ask the user for a true host fix.{% if profile_has_wait %} Then `wait(resume=true)` when waiting on the human.{% endif %}
{% endif %}
{% if evidence_digest %}
{{ evidence_digest }}

{% endif %}
Do not paste implementation in coordinator prose.{% if completed_ago %} · finished {{ completed_ago }}{% endif %}
