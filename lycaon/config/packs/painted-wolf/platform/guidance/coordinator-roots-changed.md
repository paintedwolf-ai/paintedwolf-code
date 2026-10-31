Folders changed: {% for c in changes %}{% if c.kind == "added" %}added {{ c.label }} → {{ c.path }}{% elif c.kind == "removed" %}removed {{ c.label }} (was {{ c.old_path }}){% elif c.kind == "moved" %}moved {{ c.label }} → {{ c.path }} (was {{ c.old_path }}){% else %}renamed {{ c.old_label }} → {{ c.label }} ({{ c.path }}){% endif %}{% if not forloop.Last %}; {% endif %}{% endfor %}{% if changes_omitted > 0 %}; {{ changes_omitted }} more{% endif %}{% if primary_changed %}{% if changes %}; {% endif %}primary is now {{ new_primary.label }} ({{ new_primary.path }}){% endif %}.
{% if after_count == 0 %}
No folder is attached now. File and command tools stay off until one is attached.
{% else %}
Current folders:
{% for r in after %}
  • {{ r.label }}  {{ r.path }}{% if r.is_primary %}  (primary){% endif %}
{% endfor %}
{% if after_omitted_count > 0 %}
{{ after_omitted_count }} more folders are omitted; the workspace block lists them all.
{% endif %}
Removed or moved paths and old labels no longer resolve; use the folders above.{% if active_root %} Relative paths and new programs start in {{ active_root.label }} ({{ active_root.path }}).{% endif %}
{% endif %}
