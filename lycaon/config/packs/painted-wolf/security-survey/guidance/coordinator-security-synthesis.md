Challenge finished — write a grounded report against the stated threat model.

Lead with the inferred or stated model (kind, who can reach it, how auth works). Then, in this order:

1. **In-scope vulnerabilities** — claims that survive skeptic challenge *under that model*. This set may be empty; qualify that conclusion by the accepted coverage.
2. **Hardening** — real improvements that are not exploitable under the stated model.
3. **Accepted residuals** — only risks *this repo* documented as accepted. Do not invent a house model.
4. **Coverage gaps** — surfaces not examined and currency not checked when web research was off; put each in `limits`. The host states failed or partial scans, incomplete legs, and unresolved claims itself. A successful empty scan is not itself a failed scan or proof of safety.

Do not pad severity levels, score excluded adversaries, or paste raw worker logs. Weigh the skeptic's per-claim `survives` / `refuted` and any web-researcher currency notes; a CVE page does not outrank `path:line` in this tree.

Carry every review outcome into `findings` and prose, keeping claim ids. Use `disposition: unresolved` for open questions, never `held` (examined and sound) or accepted risk. Unresolved findings have no rating answers. State material limitations and use the accepted coverage assessment. The host states each rated finding's level from its answers, so leave out `severity`.

Answer without tools using the Report document fence. Prior claim links and set-asides count toward accounting. Add `set_asides` only for remaining groups, as `{scanner, paths, reason}` or `{scan_group_ids, reason}`; the reason must cover every selected group. Zero findings need none.

{% if review_verdict and review_verdict.survey_claims %}
**Stamped claims**
Threat model: {{ review_verdict.survey_claims.threat_model }}

{% endif %}{% if review_verdict and review_verdict.survey_challenged %}
**Challenge outcomes** (including unresolved questions)

{{ review_verdict.survey_challenged.challenges }}

{% endif %}{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}

The host advances when the report is complete.
