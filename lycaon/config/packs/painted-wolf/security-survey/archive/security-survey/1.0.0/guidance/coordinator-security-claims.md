Parallel survey finished — stamp the candidate claims before anyone challenges them.

The host digest below is compiled from worker closeout citations and the scan ledger for this run. It is not the Findings plane (`record_finding` peer notes). The stamped threat model is the rubric — not a generic web checklist.

1. Take material candidate claims from the digest and worker envelopes. Skip informational model facts (`claim: model`). Merge overlapping surveyor notes into one claim.
2. Each claim is one adversary + one surface + the precondition it needs + `path:line`, with a plain one-line `title` and `status: claimed`. Empty is valid — use `claims: []`. Include `scan_group_ids` (the `group:…` ids from `scan_query` view `groups`, never a scan id) for inventory groups the claim assesses.
{% if rating_questions %}3. For a claim that a flaw exists, add `answers`:
{{ rating_questions }}
{% endif %}
Record the list with `submit_verdict`. This is the brief the later reviewers receive. Do **not** wrap the user's original survey ask as a proposition, and do **not** dispatch `skeptic` or `web-researcher` in this phase.

`submit_verdict(verdict={"verdict": "CLAIMED", "threat_model": "…", "claims": [{"id": "c1", "title": "…", "status": "claimed", "statement": "…", "cited_evidence": [{"path": "…", "line": 12}]}]}, cited_evidence=[…])`

`claims` is a JSON array — one entry per claim, each citing the `path:line` evidence that backs it. A terminal verdict must ground: cite only paths and lines the digest or worker envelopes actually name. An invented path or handle rejects the submission with the offending token; correct it and resubmit.

The host advances on that terminal verdict — do not call the phase-advance tool.

{% if evidence_digest %}
{{ evidence_digest }}

{% elif topology_output %}
{{ topology_output }}
{% endif %}
