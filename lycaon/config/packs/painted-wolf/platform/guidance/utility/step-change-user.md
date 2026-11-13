{% if command %}Command:
{{ command }}

{% endif %}{% if movement %}Git movement:
{{ movement }}

{% endif %}{% if diffs %}Changes (bounded; may be cut off):
{{ diffs }}
{% endif %}
