{% if steps %}Steps:
{{ steps }}

{% endif %}{% if receipts %}Execution receipts:
{{ receipts }}

{% endif %}{% if output %}Retained output (bounded; may be cut off):
{{ output }}

{% endif %}{% if call_result %}Tool result (bounded observation):
{{ call_result }}
{% endif %}
