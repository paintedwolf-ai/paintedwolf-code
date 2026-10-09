Challenge finished — write a grounded report against the stated threat model.

Lead with the inferred or stated model (kind, who can reach it, how auth works). Then, in this order:

1. **In-scope vulnerabilities** — claims that survive skeptic challenge *under that model*. This set may be empty. Empty is a successful closeout.
2. **Hardening** — real improvements that are not exploitable under the stated model.
3. **Accepted residuals** — only risks *this repo* documented as accepted. Do not invent a house model.
4. **Coverage gaps** — surfaces not examined and currency not checked when web research was off; put each in `limits`. The host states failed or partial scans, incomplete legs, and unresolved claims itself. A successful empty scan is not itself a failed scan or proof of safety.

Do not fill High / Medium / Low to make the report look complete. Do not score a finding that needs an excluded adversary. Do not paste raw worker logs. Weigh the skeptic's per-claim `survives` / `refuted` and any web-researcher currency notes; a CVE page does not outrank `path:line` in this tree.

Carry every review outcome into `findings` as well as the prose, keeping claim ids; every open or overturned claim needs a finding with its id. The host states each rated finding's level from its answers, so leave out `severity`.

Answer without tools, with the report fence shown under Report document. Set aside scanner groups that cannot change the rating, such as test fixtures; claim links and the challenge's set-asides already count. Scanners with zero findings need no set-asides.

{% if review_verdict and review_verdict.survey_claims %}
**Stamped claims**
Threat model: {{ review_verdict.survey_claims.threat_model }}

{{ review_verdict.survey_claims.claims }}

{% endif %}{% if review_verdict and review_verdict.survey_challenged %}
**Challenge outcomes** (per claim id: `survives` / `refuted`, with the skeptic's case)

{{ review_verdict.survey_challenged.challenges }}

{% endif %}{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}

When the summary is complete, the host advances the report phase automatically — do not call the phase-advance tool from coordinator prose.
