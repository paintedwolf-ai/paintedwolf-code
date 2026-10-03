Challenge stamped claims and independently assess run-bound dependency advisories. Preserve the original user ask.

Workers start cold: brief them on the threat model and numbered claims. Preserve user limits in `brief.constraints`. If required review conflicts, `ask_user` before dispatch; available reviewers grant no exception.

1. Dispatch `skeptic` with `scope.mode: read` to argue the strongest evidence-backed case **against each claim** under the stamped threat model. Require it to revisit constructors/defaults, alternate callers, physical enforcement points, error paths, and bypass routes; the existence or name of an intended-control helper does not refute a claim. Per-claim `survives` / `refuted`. `done_when` is that verdict list plus `path:line` sources. It also answers questions only the code settles, such as whether a flagged package is imported.
{% if spawnable_reviewers %}
2. In the **same assistant turn**, dispatch `web-researcher` to check claim currency and independently assess dependency advisories. The host attaches run-bound groups to the assignment. Page through the inventory with `scan_query`; the initial sample may be incomplete. Check affected coordinates, published fixes, current primary sources and applicability. Return each assessed group id, its conclusion, unresolved questions, and verbatim fetched URLs. An advisory omitted by the coordinator is still in scope. It cannot read the project.
{% else %}
2. Web research is off this turn — do not `task(web-researcher)`. Currency stays a coverage caveat for the report.
{% endif %}
3. Call `wait(timeout_ms=1800000, conditions=[{"kind":"all_workers_idle"}])`. Then record the outcome per claim:

   `submit_verdict(verdict={"verdict": "CHALLENGED", "challenges": [{"id": "c1", "status": "survives", "statement": "…", "scan_group_ids": ["group:…"], "cited_evidence": [{"path": "…", "line": 12}]}], "set_asides": [{"scanner": "…", "paths": ["**/testdata/**"], "reason": "detection fixtures"}]}, cited_evidence=[…]{% if spawnable_reviewers %}, cited_urls=[…]{% endif %})`

   `challenges` is a JSON array with one entry per stamped claim id, plus independent advisory assessments with new ids and a `title`. Use `status`: {{ claim_statuses|join:" | " }}; explain the evidence and remaining uncertainty. Include `scan_group_ids` (`group:…`, never a scan id) for assessed inventory groups and preserve those links from earlier claims. Do not claim to have assessed groups merely because they were listed. Missing outcomes remain unresolved in the report. The claims phase already set fixture groups aside; its set-asides already count, so do not restate them — add set-asides here only for groups still unaccounted, as `{scanner, paths, reason}` (covers every group that scanner reported entirely inside the path globs) or `{scan_group_ids, reason}`. A group reported partly in fixtures and partly in real code cannot be set aside by paths — assess it in a finding or set it aside by `scan_group_ids`. Before submitting, account for every remaining scan group; the host holds this phase until the accounting is complete. Locationless groups require explicit `scan_group_ids`. Scanners with zero findings need no set-asides.{% if rating_questions %} For each flaw claim, restate `answers` as the evidence leaves them (`unknown` only where open):
{{ rating_questions }}{% endif %} `cited_evidence` is the reviewer's observed `path:line`. The host checks reviewer envelopes and citation provenance, not conclusion truth. Include reviewer evidence{% if spawnable_reviewers %} and verbatim fetched URLs in `cited_urls`{% endif %}; correct rejected references without inventing replacements.

Leave severity and vulnerability / hardening classification to the report.

{% if review_verdict and review_verdict.survey_claims %}
**Stamped claims**
Threat model: {{ review_verdict.survey_claims.threat_model }}

{{ review_verdict.survey_claims.claims }}

{% endif %}{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}

The host advances on a terminal `submit_verdict` — do not call the phase-advance tool.
