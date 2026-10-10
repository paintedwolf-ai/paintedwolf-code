Challenge stamped claims and run-bound dependency advisories. Preserve user limits in `brief.constraints`. If required review conflicts, `ask_user` before dispatch; available reviewers grant no exception.

Read `pack_board(review_view: summary)` for exact review work IDs and current context. Dispatch each review with its supplied `workflow_work_id`.

1. Brief `skeptic` (`scope.mode: read`) on the threat model and claims. Require the strongest evidence-backed case against each: revisit defaults, alternate callers, enforcement, errors, and bypasses. Test whether proposed controls distinguish the stated adversary and whether every relevant caller reaches them. Challenge exclusions and coverage judgments as well as claims. `done_when`: per-claim survives/refuted with observed `path:line`. Assign code-only applicability questions here.
{% if spawnable_reviewers %}2. In the same assistant turn, brief `web-researcher` on claims and independent dependency review. The host attaches run-bound groups; page through `scan_query`, not just its sample. Check coordinates, published fixes, primary sources, and applicability. Return assessed group ids, conclusions, uncertainty, and verbatim fetched URLs. Coordinator omissions remain in scope. It cannot read project code.
{% else %}2. Web research is off; do not dispatch it. Retain currency as a report coverage gap.
{% endif %}3. Wait for reviewer results, then reconcile the claims and current coverage facts. Local dependency imports and reachability require repository evidence; web research establishes advisory facts and prerequisites.
{% if review_followup_attempts %}
For each unresolved claim, submit `NEEDS_INVESTIGATION` with `question: {missing_fact, obligations: [affected host obligation ids]}` on that claim. Use the returned question id verbatim as `workflow_work_id`; preserve its registered scope. Dispatch a focused read task for the missing fact, carrying existing evidence and user constraints; then dispatch `skeptic` with the returned `review_work_id` to reassess the affected items. The host retains applicable prior reviews; reconcile their conclusions with the new evidence.

Each question allows {{ review_followup_attempts }} completed investigation attempts; provider failures and rejected calls do not consume them. Assess every registered question in coverage. An open question cannot be `covered`; `immaterial` needs evidence bounding its consequences. Material or essential uncertainty also leaves its affected obligations open. Submit `CHALLENGED` only after resolution, evidenced immateriality, exhausted investigation, or a host-recorded blocker.
{% endif %}

`challenges` needs one entry per stamped id; independent advisory assessments need new ids and titles. Use status {{ claim_statuses|join:" | " }}, evidence, and uncertainty. Preserve assessed `scan_group_ids` (`group:…`, never scan ids); listing a group is not assessment. Missing outcomes stay unresolved.

Inherited claim links and set-asides already count. Account for remaining groups before terminal submission. Select whole groups with `{scanner, paths, reason}` or explicit `{scan_group_ids, reason}`; the reason must cover every selected group.
{% if rating_questions %}Restate each flaw's `answers` from the evidence, using `unknown` only where open:
{{ rating_questions }}
{% endif %}
Cite reviewer-observed paths/lines{% if spawnable_reviewers %} and verbatim fetched URLs{% endif %}; repair rejected references without inventing replacements. Provenance checks do not decide truth. The report assigns severity. The host advances on the terminal verdict.

{% if review_verdict and review_verdict.survey_claims %}
**Stamped claims**
Threat model: {{ review_verdict.survey_claims.threat_model }}

{{ review_verdict.survey_claims.claims }}

{% endif %}{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}
