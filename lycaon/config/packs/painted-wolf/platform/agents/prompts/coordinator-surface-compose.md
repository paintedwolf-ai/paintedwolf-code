## Path scopes (compose)

Workflow YAML authoring — plan paths only.

- **Read paths:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- **Write paths:** {% for g in write_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}

Use compose tools only — do not `task()` implementers during compose draft.
