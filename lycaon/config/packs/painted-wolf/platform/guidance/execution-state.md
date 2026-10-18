{% if view == "instructions" %}Revisit rejected operations still needed; later prerequisite calls did not execute them. Other work may supersede them. Invocation completion is not a passing check. Verdicts cover their command and source; later changes need fresh verification. Report observed outcomes and limitations.
{% else %}## Execution observations

Bounded observations from retained history for this user intent; omitted fields are unknown.
{% if operations %}Rejected operations without a later successful call with matching tool and arguments:
{% for row in operations %}- {{ row.tool }} call={{ row.call_id }}{% if row.path %} path={{ row.path }}{% endif %} did not execute ({{ row.code }}).{% if row.checklist_after %} Later checklist update completed.{% endif %}{% if row.tool_request_after %} Later tool request completed; consult current schemas.{% endif %}
{% endfor %}{% if operations_omitted %}Earlier rejected operations omitted: {{ operations_omitted }}.
{% endif %}{% endif %}{% if executions %}Latest execution receipts, oldest first:
{% for row in executions %}- {{ row.tool }} call={{ row.call_id }} status={{ row.status }} invoked={{ row.invoked }}{% if row.code %} code={{ row.code }}{% endif %}{% if row.command %} command={{ row.command }}{% endif %}{% if row.cwd %} cwd={{ row.cwd }}{% endif %}{% if row.resolvable %} resolvable_executable={{ row.resolvable }}{% endif %}{% if row.source_verdict %} verdict={{ row.source_verdict }} source={{ row.source_revision }} root={{ row.source_root_digest }}{% endif %}
{% endfor %}{% if executions_omitted %}Earlier execution receipts omitted: {{ executions_omitted }}.
{% endif %}
{% if declared_command %}Declared project check: {{ declared_command }}.
{% endif %}{% endif %}

{% endif %}
