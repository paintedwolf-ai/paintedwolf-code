## Path scopes (plan workflow)

Governing blueprint paths — `${overlay_dir}/blueprints/**`.

- **Read paths:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- **Write paths:** {% for g in write_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}

Plan phases gate which tools are callable — follow the mode instructions for the current phase.
