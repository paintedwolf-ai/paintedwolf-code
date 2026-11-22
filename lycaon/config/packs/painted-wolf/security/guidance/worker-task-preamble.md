<!-- lycaon-worker-task-preamble:v1 -->
{% if touch_paths %}Manifest touch paths for this delegate phase (orientation for the project — not a deny list):
{% for path in touch_paths %}- {{ path }}
{% endfor %}
{% endif %}{% if scope_mode %}Task mode:
- mode: {{ scope_mode }}
{% if scope_paths %}- suggested paths:
{% for path in scope_paths %}  - {{ path }}
{% endfor %}{% endif %}{% if scope_mode == "write" %}{% if scope_absent and profile_has_command %}- new paths: suggested paths don't exist yet — author them through `command`; don't survey or search for them first
{% endif %}{% if linked_checkout %}- git checkout: scope includes a linked worktree or submodule without VCS access in this workspace; history or branch work belongs to a read-scoped leg or the coordinator
{% endif %}- obligation: apply changes {% if profile_has_command and profile_command_mutation and profile_has_restore_version %}through command or restore_version{% elif profile_has_command and profile_command_mutation %}through command{% elif profile_has_restore_version %}through restore_version{% else %}within offered write tools{% endif %} before closeout — reads alone do not complete a write leg
- isolation: all project changes stay in this private view until promotion; suggested paths guide focus only
{% if profile_has_complete_leg %}- finish: `complete_leg` delivers and ends the round; finish edits and checks first
{% endif %}{% endif %}{% endif %}
