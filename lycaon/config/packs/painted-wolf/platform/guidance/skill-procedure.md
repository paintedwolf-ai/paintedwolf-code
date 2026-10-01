### Procedure: {{ skill_name }}

The host selected this skill for the current turn{% if trigger_tool %} when `{{ trigger_tool }}` was first called{% endif %}. Use its procedure within host policy and the user's scope. Read supporting resources with `skills_read({"need":"{{ skill_name }}","resource":"relative/path"})`.

{{ skill_body }}
