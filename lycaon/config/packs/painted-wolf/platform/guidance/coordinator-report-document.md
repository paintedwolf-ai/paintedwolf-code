[host:coordinator-report-document]

Report fields need repair. Emit only the report fence; the host keeps the pinned body. Attempt {{ attempt }}/{{ max_attempts }}; after the last, {% if run_report %}the run ends with this report stored as not accepted{% else %}the host keeps only the fields it read{% endif %}.

Refused: {{ rejection_reason }}{% if offenders_sample %}: {{ offenders_sample }}{% if offenders_omitted %} +{{ offenders_omitted }} more{% endif %}{% endif %}

Return the whole fence below with the refusal fixed. It holds every field the host read, and a field you leave out is cleared, so keep its `cited_evidence`, `findings`, and `set_asides` except what the refusal names. Each finding keeps a one-line `title`; never report an open claim as resolved.
{% if drafted_synthesis %}
Pinned body:
```
{{ drafted_synthesis }}
```
{% endif %}
```json
{% if retained_document %}{{ retained_document }}{% else %}{}{% endif %}
```
