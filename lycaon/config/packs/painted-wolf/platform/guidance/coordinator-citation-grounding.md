[host:coordinator-citation-grounding]

Emit only the citations fence; the host keeps the pinned body. Attempt {{ attempt }}/{{ max_attempts }}.

Rejected: {{ offenders_sample }}
Observed handles: {{ observed_handles_sample }}
Observed paths: {{ observed_paths_sample }}
Observed URLs: {{ observed_urls_sample }}
URLs printed by commands are output, not observed web pages. Cite the command handle for its recorded outcome; do not put output URLs in `cited_urls`.

Keep accepted citations. Copy exact handles; do not construct a handle from a tool name or call count. An explicit handle cites the captured observation, even when `superseded_by` names newer file evidence. Use the newer observation for current file contents. Git and command receipts remain evidence of their recorded outcomes after files change.
{% if repair_observations %}
Relevant observations (choices, not inferred support for your report):
{{ repair_observations }}
{% endif %}
Use `recall` to retrieve an existing observation if needed. Do not repeat a write, commit, merge, or other effect to obtain citations. For a file claim, include path, line, and excerpt from its observation. Remove citations you cannot support.
{% if drafted_synthesis %}
Pinned body:
```
{{ drafted_synthesis }}
```
{% endif %}
```json
{% if retained_citations %}{{ retained_citations }}{% else %}{"cited_evidence":[],"cited_urls":[],"artifact_ids":[]}{% endif %}
```
