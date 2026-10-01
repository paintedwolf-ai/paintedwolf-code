## Path scopes (spec posture)

Read-heavy coordinator path policy for specify-mode chat.

- **Read paths:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- **Write paths:** {% for g in write_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}

Spec posture blocks delegation and state tools until plan workflow gates allow — branch on `SPEC_POSTURE_*` codes.
