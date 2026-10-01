<!-- lycaon-worker-task-preamble:v1 -->
{% if touch_paths %}Manifest touch paths for this delegate phase (orientation for the project — not a deny list):
{% for path in touch_paths %}- {{ path }}
{% endfor %}
{% endif %}{% if scope_mode %}Task mode:
- mode: {{ scope_mode }}
{% if scope_paths %}- suggested paths:
{% for path in scope_paths %}  - {{ path }}
{% endfor %}{% endif %}{% if scope_mode == "write" %}{% if scope_absent %}- new paths: suggested paths don't exist yet — author them with write; don't survey or search for them first
{% endif %}- obligation: apply changes with write, edit, replace_lines, or restore_version before closeout — reads alone do not complete a write leg
- isolation: all project changes stay in this overlay until promotion; suggested paths guide focus only
- finish: the last iteration offers only `complete_leg`; finish edits and checks first
{% endif %}{% endif %}
