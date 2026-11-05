[host:coordinator-citation-grounding]

Your answer is retained, and its valid citations stay as they are. Repair only what is listed. Attempt {{ attempt }}/{{ max_attempts }}.

Rejected: {{ offenders_sample }}
{% if repair_targets %}Citations to replace:
{% for target in repair_targets %}- {{ target.id }}: `{{ target.token }}` ({{ target.defect }}){% if target.web_link %}, a web link: give an observed URL{% endif %}{% if target.candidates %}; more than one observed path ends this way, so name one: {{ target.candidates }}{% endif %}{% if target.use %}; sendable: `[cite: {{ target.id }} => {{ target.use }}]`{% endif %}{% if target.worker_output %}, because that worker's output showed no numbers for these lines and you cannot re-read for it{% elif target.unnumbered %}, from output with no line numbers: re-read with them, quote the line as `[path:line "text"]`, or cite the path with no line{% endif %}
{% endfor %}{% endif %}
Send one directive per citation and nothing else, such as `[cite: c1 => read#5:42]`, `[cite: c1 => path/to/file]`, or `[cite: c2 => <https://example.test/observed-page>]` for a web link. A link keeps its label. Or return the same answer with only its citations corrected.
{% if document_citation_targets %}Report fence citations to replace:
{% for target in document_citation_targets %}- {{ target }}
{% endfor %}
Return the report fence with those `where` entries corrected.
{% if retained_document %}
```json
{{ retained_document }}
```
{% endif %}{% endif %}
Observed handles: {{ observed_handles_sample }}{% if observed_handles_omitted %} (+{{ observed_handles_omitted }} more){% endif %}
Observed paths: {{ observed_paths_sample }}{% if observed_paths_omitted %} (+{{ observed_paths_omitted }} more){% endif %}
Observed URLs: {{ observed_urls_sample }}{% if observed_urls_omitted %} (+{{ observed_urls_omitted }} more){% endif %}
URLs printed by commands are output, not observed web pages. Cite the command handle for its recorded outcome; do not link output URLs as sources.

Copy exact handles; do not construct a handle from a tool name or call count. An explicit handle cites the captured observation, even when `superseded_by` names newer file evidence. Use the newer observation for current file contents. Git and command receipts remain evidence of their recorded outcomes after files change.
{% if can_recall %}When an observation is no longer in context, recover it with `recall(query="handle:read#3")`, passing its exact handle alone, or a `path:` query, then copy the line from the returned body, not from memory.{% endif %}
{% if repair_observations %}
Observed repair choices (not inferred support for your report). Cite a choice without `lines` by its bare handle, with no line number:
{{ repair_observations }}
{% endif %}
{% if resume_work_allowed %}A tool that only reads keeps the retained answer, so you can still send directives after it. If the report needs more work, use a tool with effects, then write a fresh report reflecting its results: this retires the retained answer, and directives no longer apply. Otherwise repair citations using existing observations.{% else %}Repair citations using existing observations.{% endif %} Do not repeat an effect to obtain citations. A citation you cannot support can be left out of a corrected answer.
{% if drafted_synthesis %}
Retained answer:
`````
{{ drafted_synthesis }}
`````
{% endif %}
