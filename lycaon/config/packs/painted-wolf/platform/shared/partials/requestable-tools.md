{% if requestable_capabilities %}### More tools

Tools cover {{ requestable_capabilities|join:", " }}. Use offered tools when sufficient. Otherwise, call `request_tools` with `need`: the missing operation and target, or an exact name from feedback. Loaded schemas, including permitted family members, arrive on the next call; neither loading nor checklist updates run tools.
{% endif %}
