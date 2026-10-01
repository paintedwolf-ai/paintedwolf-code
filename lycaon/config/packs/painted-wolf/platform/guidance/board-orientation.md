{% if root_count == 0 %}{% else %}
{% if include_scan_legend %}{% include "partials/pack-board-legend.md" %}{% endif %}

<!-- lycaon-board-orientation:v1 -->
## Pack board
{% if worker_workspace_path %}
Workspace: {{ worker_workspace_path }}
{% if inventory_at %}File inventory observed at {{ inventory_at }}. Later tool receipts supersede this inventory.
{% endif %}{% endif %}

{% autoescape off %}{{ pack_sentinel }}
{% endautoescape %}{% if now_line %}{{ now_line }}
{% endif %}{% if multi_root %}Project spans {{ root_count }} folders.
{% for root in orientation_roots %}## {% if root.is_primary %}{{ root.label }} (primary){% else %}@{{ root.label }}{% endif %}
{% for line in root.lines %}{{ line.text }}
{% endfor %}{% if root.truncated %}(brief trimmed to fit budget)
{% endif %}{% endfor %}{% if root_omitted_count > 0 %}{{ root_omitted_count }} folders omitted; use workspace tools for the complete set.
{% endif %}Use @<label>/path to address a specific folder.
{% endif %}{% for line in lines %}{{ line.text }}
{% endfor %}
{% endif %}
