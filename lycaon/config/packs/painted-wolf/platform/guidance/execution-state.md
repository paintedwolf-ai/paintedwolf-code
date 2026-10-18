## Execution observations

Bounded observations from retained history for this user intent; omitted fields are unknown.
{% if operations %}Rejected operations without an observed successful exact retry:
{% for row in operations %}- {{ row.tool }} call={{ row.call_id }}{% if row.path %} path={{ row.path }}{% endif %} did not execute ({{ row.code }}).
{% endfor %}{% if operations_omitted %}Earlier rejected operations omitted: {{ operations_omitted }}.
{% endif %}{% if checklist_updated %}A checklist update completed. It did not execute these calls. Revisit their original arguments before continuing; another operation may have superseded them.
{% endif %}{% endif %}{% if executions %}Latest execution receipts:
{% for row in executions %}- {{ row.tool }} call={{ row.call_id }} status={{ row.status }} invoked={{ row.invoked }}{% if row.code %} code={{ row.code }}{% endif %}{% if row.command %} command={{ row.command }}{% endif %}{% if row.cwd %} cwd={{ row.cwd }}{% endif %}{% if row.resolvable %} resolvable_executable={{ row.resolvable }}{% endif %}{% if row.source_verdict %} verdict={{ row.source_verdict }} source={{ row.source_revision }} root={{ row.source_root_digest }}{% endif %}
{% endfor %}{% if executions_omitted %}Earlier execution receipts omitted: {{ executions_omitted }}.
{% endif %}Invocation completion alone is not a passing check. A recorded verdict covers its command and source only; later changes need fresh verification. Rejected attempts provide no passing result. Recover or report the limitation accurately.
{% if declared_command %}Declared project check: {{ declared_command }}.
{% endif %}{% endif %}
