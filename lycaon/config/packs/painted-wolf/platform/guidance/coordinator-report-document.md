[host:coordinator-report-document]

Report fields need repair. Emit only the report fence; the host keeps the pinned body. Attempt {{ attempt }}/{{ max_attempts }}; after the last, {% if run_report %}the run ends with this report stored as not accepted{% else %}the host keeps only the fields it read{% endif %}.

{% if offending_keys %}Remove these unrecognized keys:
{% for k in offending_keys %}- `{{ k }}`
{% endfor %}
{% endif %}{% if allowed_top_level_keys %}Allowed top-level report keys: {{ allowed_top_level_keys | join: ", " }}.
Do not add any other keys. Omit optional fields with null or empty values.
{% endif %}
{% if document_issues %}Repair every listed defect together:
{% for issue in document_issues %}- {{ issue.Reason }}{% for offender in issue.Offenders %}
  - {{ offender }}{% endfor %}
{% endfor %}{% else %}Refused: {{ rejection_reason }}{% if offenders_sample %}: {{ offenders_sample }}{% if offenders_omitted %} +{{ offenders_omitted }} more{% endif %}{% endif %}{% endif %}

The host has pinned your markdown report body{% if pinned_body_chars %} ({{ pinned_body_chars }} characters){% endif %}. Do not repeat the report narrative. Return only the ```json ... ``` fence below with these defects fixed. It holds every field the host read, and a field you leave out is cleared, so keep its `cited_evidence`, `findings`, and `set_asides` except the defective fields. Each finding keeps a one-line `title`; never report an open claim as resolved:

```json
{% if retained_document %}{{ retained_document }}{% else %}{}{% endif %}
```
