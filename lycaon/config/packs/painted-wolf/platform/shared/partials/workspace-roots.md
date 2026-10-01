{% if root_count == 0 %}
No folder is attached. File and shell tools stay off until the user attaches one (Folders button); chat and web research still work.
{% elif root_count == 1 %}
Workspace: {{ project_dir }}
{% else %}
This project spans {{ root_count }} folders:
{% for r in workspace_roots %}
  • {{ r.label }}      {{ r.path }}{% if r.is_primary %}        (primary){% endif %}{% if r.is_active %}        (session folder){% endif %}
{% endfor %}
{% if workspace_roots_omitted_count > 0 %}
{{ workspace_roots_omitted_count }} additional folders are omitted from this prompt view; use the workspace tools for the complete set.
{% endif %}
{% include "partials/multi-root-addressing.md" %}
{% endif %}
{% if root_count > 0 %}
User absolute paths: call `read`/`list_dir`/`grep` with them — expect an Approvals card or `Code:`; never refuse in prose or ask them to paste the file.
{% endif %}
