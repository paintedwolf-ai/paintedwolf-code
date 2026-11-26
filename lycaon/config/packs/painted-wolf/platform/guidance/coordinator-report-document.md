[host:coordinator-report-document]

Report fields need repair. To repair them, emit only the report fence; the host keeps your Markdown answer and its citations. Attempt {{ attempt }}/{{ max_attempts }}; after the last, {% if run_report %}the run ends with this report stored as not accepted{% else %}the host keeps only the fields it read{% endif %}.
{% if resume_work_allowed %}If the work is not finished, use an offered tool instead, then write a fresh report reflecting its results. This retires the pinned body.
{% endif %}
{% if offending_keys %}Remove these unrecognized keys:
{% for k in offending_keys %}- `{{ k }}`
{% endfor %}
{% endif %}{% if allowed_top_level_keys %}Allowed top-level report keys: {{ allowed_top_level_keys | join: ", " }}.
Do not add any other keys. Omit optional fields with null or empty values.
{% endif %}
{% if document_issues %}Repair every listed defect together:
{% for issue in document_issues %}- {{ issue.reason }}{% for offender in issue.offenders %}
  - {{ offender }}{% endfor %}
{% endfor %}{% else %}Refused: {{ rejection_reason }}{% if offenders_sample %}: {{ offenders_sample }}{% if offenders_omitted %} +{{ offenders_omitted }} more{% endif %}{% endif %}{% endif %}

The host has kept your Markdown answer{% if pinned_body_chars %} ({{ pinned_body_chars }} characters){% endif %}. Do not repeat it. Return only a `json` report fence with these defects fixed. Each item in `findings` keeps a one-line `title`, and its `where` entries stay typed `{evidence, path, line, end_line, excerpt}`; `set_asides` keep their reasons; never report an open claim as resolved.
{% if retained_document %}
This fence holds every field the host read, and a field you leave out is cleared, so keep everything except the defective fields:

```json
{{ retained_document }}
```
{% else %}
The host read no report fields yet, so write the fence with the fields listed above.
{% endif %}
