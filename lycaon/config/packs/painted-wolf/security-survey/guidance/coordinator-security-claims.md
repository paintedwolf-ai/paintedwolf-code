Stamp candidate claims from the worker closeouts and run scan ledger below. This digest is not `record_finding` peer notes. Use the stamped threat model, not a generic checklist.

1. Merge overlapping material claims; skip informational `claim: model` facts. Each claim needs an id, one-line `title`, `status: claimed`, adversary, surface, precondition, and observed `path:line` evidence.
2. Page through `scan_query` view `groups`. Link assessed groups using `scan_group_ids` (`group:…`, never scan ids).
3. Record remaining nonmaterial groups in `set_asides`: `{scanner, paths, reason}` selects groups wholly inside those globs; `{scan_group_ids, reason}` selects explicit groups. Mixed fixture/real-code or locationless groups need assessment or explicit ids. The reason must hold for every selected group. Zero findings need no set-asides. Challenge inherits this accounting.
{% if rating_questions %}4. Flaw claims need `answers`:
{{ rating_questions }}
{% endif %}
Use `submit_verdict` with `verdict: CLAIMED`, `threat_model`, `claims`, and `set_asides`; both lists may be `[]`. Ground `cited_evidence` in the digest or worker envelopes. Correct rejected references without inventing replacements.

These claims brief the reviewers. Do not restate the user's ask as a proposition or dispatch reviewers here. The host advances on the terminal verdict.

{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}
