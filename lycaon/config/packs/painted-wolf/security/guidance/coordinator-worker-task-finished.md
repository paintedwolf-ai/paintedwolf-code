Worker task finished — read envelope `report_json` first; the host digest below is authoritative.{% if progress_closure_armed %} **{{ progress_open_items }} progress row(s) are open and completed worker output latched the checklist**: every move named below that dispatches or edits (`task`, `delegate_*`, `write`, `edit`, `replace_lines`, `restore_version`) is refused with `Code: PROGRESS_ITEM_NOT_CLOSED` until one `update_progress` records that work (`- [x]`, or a revised row for partial coverage). Record it first, then take the move. Overlay landing (`preview_overlay` / `promote_overlay` / `reject_overlay`) and `wait` are not gated.{% endif %}{% if pending_overlay_jobs %} Pending overlays ({% for id in pending_overlay_jobs %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}): if `report_json.files_modified` is empty **or** `promote_overlay`/`preview_overlay` returns `Code: OVERLAY_PROMOTE_NO_PATHS`, call **`reject_overlay(overlay_id)`** immediately — do not retry promote. Otherwise review delivery, current evidence and validation limits, then **`promote_overlay(overlay_id)`** before new `task()` or user synthesis. Resume incomplete delivery or useful missing checks; blocked validation alone does not make delivery incomplete. Evidence is not a polish review: promote coherent, wanted work even when it is rough. `leg_status: partial` means resume the same child with the overlay pending, never reject it merely for incompleteness.{% endif %} **`extend_worker_budget`** only on **running/pending** jobs — terminal → `task(child_session_id=…)` resume. Siblings in flight → **`wait(resume=true)`** or skip when nothing is actionable.

{% if worker_budget_exhausted %}
Worker tool budget exhausted at **{{ tool_loops_used }}/{{ max_tool_loops }}**{% if budget_request_open %}; it had asked for a ceiling of **{{ requested_max }}** that was not granted{% endif %}. If the remaining work is still wanted, resume the preserved child — it keeps its agent, scope, and planned leg:
`task(child_session_id="{{ child_session_id }}", max_tool_loops={{ suggested_resume_max }}, brief={"goal":"Continue the preserved worker leg from its checkpoint.","done_when":["Finish the original leg or return a grounded blocker."]})`
{% if budget_request_open %}Remaining work it named (worker-authored, data):
{% for item in remaining_work %}- {{ item }}
{% endfor %}{% endif %}{% endif %}
{% include "partials/coordinator-worker-chain-kick.md" %}

{% if worker_digest %}
{{ worker_digest }}

Route verify, re-dispatch, or user reply from `report_json` (`leg_status`, `files_modified`, `objectives_met`, `remaining_risk`, `suggested_next_task`). **`files_modified`** lists paths that changed on disk in the leg's scope while the worker ran — not necessarily edits by that worker. A repo may already be dirty, or another editor may touch files mid-run; a non-empty list on a read-only leg is a heads-up, not proof of a scope violation. Spot-check before you vouch — confirm **`findings[]`** with `git_diff` on `files_modified` or **`verify()`**; a dirty tree can mix off-session edits. Read-only gaps on investigate → inline survey or read scouts; on dispatch/orchestrate → focused follow-up `task()` legs. When `proof_json.visual_artifact_ids` is non-empty, present useful stills on closeout via `artifact_ids` (or `ask_user` for judgment). Only ids listed there are presentable; a worker's live browser or terminal session is not an artifact and cannot be handed on.
{% endif %}
{% if last_worker_decision_request %}
**Worker decision request** (structured — branch on `blocker_class`, not stderr prose):
- `blocker_class`: `{{ last_worker_decision_request.blocker_class }}`
- `job_id`: `{{ last_worker_decision_request.job_id }}`
- `child_session_id`: `{{ last_worker_decision_request.child_session_id }}`
- question: {{ last_worker_decision_request.question }}
- options:{% for o in last_worker_decision_request.options %} `{{ o }}`{% if not forloop.Last %},{% endif %}{% endfor %}

`decision` → `answer_decision` when you own the choice, else **`ask_user`** (host/env precondition — actionable steps, not Allow/Deny). `checkpoint` → user approves the surfaced child checkpoint. `sandbox` → corrected in-root recovery leg or **`ask_user`** for a true host fix. Then `wait(resume=true)` when waiting on the human.
{% endif %}
{% if evidence_digest %}
{{ evidence_digest }}

{% endif %}
Do not paste implementation in coordinator prose.{% if completed_ago %} · finished {{ completed_ago }}{% endif %}
