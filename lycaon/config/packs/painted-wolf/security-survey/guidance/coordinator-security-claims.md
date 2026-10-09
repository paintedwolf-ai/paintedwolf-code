Stamp candidate claims from the worker closeouts and run scan ledger below. This digest is not `record_finding` peer notes. Use the stamped threat model, not a generic checklist.

1. Merge overlapping material claims; skip informational `claim: model` facts. Each claim's `statement` names adversary, surface, and precondition; use only the fields the verdict shape lists.
2. Account for the complete inventory through `scan_query` view `groups`, following every continuation at the same inventory revision. Samples do not assess omitted groups. Link assessed groups using `scan_group_ids`; keep inconclusive groups as unresolved candidate claims.
3. Record remaining nonmaterial groups in `set_asides`: `{scanner, paths, reason}` selects groups wholly inside those globs; `{scan_group_ids, reason}` selects explicit groups. Mixed fixture/real-code or locationless groups need assessment or explicit ids. The reason must hold for every selected group. Zero findings need no set-asides. Use view `accounting` to preview selectors and distinguish accepted accounting from the last candidate. Challenge inherits accepted accounting.
{% if rating_questions %}4. Flaw claims need `answers`:
{{ rating_questions }}
{% endif %}
Submit through `submit_verdict`, whose schema gives this phase's shape, with `verdict: CLAIMED`, assessing every current host fact at its revision. Copy fact ids unchanged, including `/`; include worker-reported gaps. Lists may be empty only when no corresponding work exists. Cite observed handles or paths/lines; scanner groups are cited through `scan_group_ids`, not as file paths. Never invent evidence.

These claims brief the reviewers. Do not restate the user's ask as a proposition or dispatch reviewers here. The host advances on the terminal verdict.

{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}
