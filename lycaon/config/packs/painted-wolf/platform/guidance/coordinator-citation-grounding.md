[host:coordinator-citation-grounding]

Your answer is retained, and its valid citations stay as they are. Repair only what is listed. Attempt {{ attempt }}/{{ max_attempts }}.

Rejected: {{ offenders_sample }}
{% if repair_targets %}Citations to replace:
{% for target in repair_targets %}- {{ target.id }}: `{{ target.token }}` ({{ target.defect }}){% if target.link %}, a link: give an observed URL{% endif %}
{% endfor %}{% endif %}
Send one directive per citation and nothing else, such as `[cite: c1 => read#5:42]`, or `[cite: c2 => <https://example.test/observed-page>]` for a link. A link keeps its label. Or return the same answer with only its citations corrected.
{% if document_citation_targets %}Report fence citations to replace:
{% for target in document_citation_targets %}- {{ target }}
{% endfor %}
Return the report fence with those `where` entries corrected.
{% if retained_document %}
```json
{{ retained_document }}
```
{% endif %}{% endif %}
Observed handles: {{ observed_handles_sample }}
Observed paths: {{ observed_paths_sample }}
Observed URLs: {{ observed_urls_sample }}
URLs printed by commands are output, not observed web pages. Cite the command handle for its recorded outcome; do not link output URLs as sources.

Copy exact handles; do not construct a handle from a tool name or call count. An explicit handle cites the captured observation, even when `superseded_by` names newer file evidence. Use the newer observation for current file contents. Git and command receipts remain evidence of their recorded outcomes after files change.
{% if can_recall %}When an observation is no longer in context, recover it with `recall(query="handle:read#3")`, passing its exact handle alone, or a `path:` query, then copy the line from the returned body, not from memory.{% endif %}
{% if repair_observations %}
Observed repair choices (not inferred support for your report):
{{ repair_observations }}
{% endif %}
{% if resume_work_allowed %}If the report needs more work, use an offered tool, then write a fresh report reflecting its results. This retires the retained answer. Otherwise repair citations using existing observations.{% else %}Repair citations using existing observations.{% endif %} Do not repeat an effect to obtain citations. A citation you cannot support can be left out of a corrected answer.
{% if drafted_synthesis %}
Retained answer:
`````
{{ drafted_synthesis }}
`````
{% endif %}
