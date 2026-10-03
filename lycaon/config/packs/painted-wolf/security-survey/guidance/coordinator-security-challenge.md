Challenge stamped claims and run-bound dependency advisories. Preserve the user ask. Preserve user limits in `brief.constraints`. If required review conflicts, `ask_user` before dispatch; available reviewers grant no exception.

1. Brief `skeptic` (`scope.mode: read`) on the threat model and claims. Require the strongest evidence-backed case against each: revisit defaults, alternate callers, enforcement, errors, and bypasses. A control helper's name proves nothing. `done_when`: per-claim survives/refuted with observed `path:line`. Assign code-only applicability questions here.
{% if spawnable_reviewers %}2. In the same assistant turn, brief `web-researcher` on claims and independent dependency review. The host attaches run-bound groups; page through `scan_query`, not just its sample. Check coordinates, published fixes, primary sources, and applicability. Return assessed group ids, conclusions, uncertainty, and verbatim fetched URLs. Coordinator omissions remain in scope. It cannot read project code.
{% else %}2. Web research is off; do not dispatch it. Retain currency as a report coverage gap.
{% endif %}3. Call `wait(timeout_ms=1800000, conditions=[{"kind":"all_workers_idle"}])`, then `submit_verdict` with `verdict: CHALLENGED`, `challenges`, `set_asides`, and `cited_evidence`{% if spawnable_reviewers %} plus `cited_urls`{% endif %}.

`challenges` needs one entry per stamped id; independent advisory assessments need new ids and titles. Use status {{ claim_statuses|join:" | " }}, evidence, and uncertainty. Preserve assessed `scan_group_ids` (`group:…`, never scan ids); listing a group is not assessment. Missing outcomes stay unresolved.

Inherited claim links and set-asides already count. Account for remaining groups before submitting; the host holds this phase otherwise. Add `{scanner, paths, reason}` only for groups wholly inside the globs, or `{scan_group_ids, reason}`. Mixed fixture/real-code and locationless groups need assessment or explicit ids; reasons must cover every selected group. Zero findings need no set-asides.
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
