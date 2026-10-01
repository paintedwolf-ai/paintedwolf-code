Project folder roots changed — review the workspace block on your next turn.

{% if added_count > 0 and before_count <= 1 and after_count >= 2 %}
A folder was added — this project now spans {{ after_count }} folders:
{% for r in after %}
  • {{ r.label }}  {{ r.path }}{% if r.is_primary %}  (primary){% endif %}
{% endfor %}
{% if after_omitted_count > 0 %}
{{ after_omitted_count }} additional folders are omitted from this wake; use the workspace tools for the complete set.
{% endif %}
Available tools retain their scope and capability limits.
{% include "partials/multi-root-addressing.md" %}
{% elif removed_count > 0 and after_count == 1 %}
A folder was removed — this project is back to a single folder: {{ after.0.path }}.
Both relative paths and @<label>/ addressing remain valid.
{% elif removed_count > 0 and after_count == 0 %}
The last folder was removed — this project has no folder attached. File tools are unavailable until you attach one.
{% elif before_count == 1 and after_count == 1 and before.0.id == after.0.id and before.0.path != after.0.path %}
The workspace is now at {{ after.0.path }}. Relative paths in this session are unchanged.
{% elif primary_changed and after_count >= 2 %}
The primary folder is now {{ new_primary.label }} ({{ new_primary.path }}).
{% endif %}
{% if active_root %}This session's active folder is {{ active_root.label }} ({{ active_root.path }}); bare relative paths resolve there.{% elif after_count > 0 %}Check this session's active folder in the workspace block before using relative paths.{% endif %}
