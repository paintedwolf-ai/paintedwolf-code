## Tool schema (profile `{{ tool_profile }}`)

{{ units.conduct }}

Schemas define arguments, capabilities, and limits. Below are required fields.

{% if profile_has_command %}
{% include "partials/command-surface.md" %}
{% endif %}

{% if agent_tools %}
### Tools

{% for t in agent_tools %}- `{{ t.name }}`{% if t.required_args %}: {% for a in t.required_args %}`{{ a }}`{% if not forloop.Last %}, {% endif %}{% endfor %}{% endif %}
{% endfor %}
{% endif %}

{% include "partials/requestable-tools.md" %}{% set skills_heading = "###" %}{% include "partials/agent-skills.md" %}{% if agent_host_resources %}{% include "partials/host-resources.md" %}{% endif %}Branch on tool **`Code:`** — never parse free-form errors alone.

Identical repeated calls → heed `DOOM_LOOP_REPEAT*` and change approach.

{% if write_globs %}
### Allowed write paths

{% for g in write_globs %}- `{{ g }}`
{% endfor %}

Use **`write`** to create new files; use **`edit`** or **`replace_lines`** on paths that already exist. Use **`restore_version`** to revert a file to a prior retained ledger version. On `Code: WRITE_SCOPE_DENIED`, pick a path matching a glob above.
{% endif %}

{% if profile_has_write_tools and profile_has_verify %}
{% include "partials/worker-write-leg-verify.md" %}
{% endif %}

{% if reject_codes %}
### Pre-empt these tool rejections

Avoid the wasted call — the rest arrive inline at reject time, keyed by `Code:`.

| Code | Do this on the first call |
|------|---------------------------|
{% for row in reject_codes %}| `{{ row.code }}` | {{ row.branch_instruction }} |
{% endfor %}
{% endif %}
