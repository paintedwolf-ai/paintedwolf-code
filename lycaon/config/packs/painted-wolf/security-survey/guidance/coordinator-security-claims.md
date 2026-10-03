Stamp candidate claims from the worker closeouts and run scan ledger below. This digest is not `record_finding` peer notes. Use the stamped threat model, not a generic checklist.

1. Merge overlapping material claims; skip informational `claim: model` facts. Each claim needs `id`, one-line `title`, `status: claimed`, and `statement` explaining adversary, surface, and precondition, with observed `cited_evidence`. Do not add undeclared fields.
2. Page through `scan_query` view `groups`. Link assessed groups using `scan_group_ids` (`group:…`, never scan ids).
3. Record remaining nonmaterial groups in `set_asides`: `{scanner, paths, reason}` selects groups wholly inside those globs; `{scan_group_ids, reason}` selects explicit groups. Mixed fixture/real-code or locationless groups need assessment or explicit ids. The reason must hold for every selected group. Zero findings need no set-asides. Challenge inherits this accounting.
{% if rating_questions %}4. Flaw claims need `answers`:
{{ rating_questions }}
{% endif %}
Submit the nested phase shape in the workflow instructions, filling every coverage assessment from current host facts. Lists may be empty only when no corresponding work exists. Cite observed handles or paths/lines; never invent evidence.

These claims brief the reviewers. Do not restate the user's ask as a proposition or dispatch reviewers here. The host advances on the terminal verdict.

{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}
