[host:coordinator-citation-grounding]

To repair citations, emit only the citations fence; the host keeps the pinned body. Attempt {{ attempt }}/{{ max_attempts }}.

Rejected: {{ offenders_sample }}
Observed handles: {{ observed_handles_sample }}
Observed paths: {{ observed_paths_sample }}
Observed URLs: {{ observed_urls_sample }}
URLs printed by commands are output, not observed web pages. Cite the command handle for its recorded outcome; do not put output URLs in `cited_urls`.

Keep accepted citations. Copy exact handles; do not construct a handle from a tool name or call count. An explicit handle cites the captured observation, even when `superseded_by` names newer file evidence. Use the newer observation for current file contents. Git and command receipts remain evidence of their recorded outcomes after files change.
{% if can_recall %}When an observation is no longer in context, recover it with `recall(query="handle:read#3")`, passing its exact handle alone, or a `path:` query, then copy the line and excerpt from the returned body, not from memory.{% endif %}
{% if repair_observations %}
Observed repair choices (not inferred support for your report):
{{ repair_observations }}
{% endif %}
{% if resume_work_allowed %}If the report needs more work, use an offered tool, then write a fresh report reflecting its results. This retires the pinned body. Otherwise repair citations using existing observations.{% else %}Repair citations using existing observations.{% endif %} For citation repair, do not repeat an effect to obtain citations. For a file claim, include path, line, and excerpt from its observation. Remove citations you cannot support.
{% if drafted_synthesis %}
Pinned body:
```
{{ drafted_synthesis }}
```
{% endif %}
```json
{% if retained_citations %}{{ retained_citations }}{% else %}{"cited_evidence":[],"cited_urls":[],"artifact_ids":[]}{% endif %}
```
