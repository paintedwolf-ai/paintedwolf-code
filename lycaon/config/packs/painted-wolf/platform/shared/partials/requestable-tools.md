{% if requestable_capabilities %}### More tools

Additional tools cover {{ requestable_capabilities|join:", " }}. Use offered tools when sufficient. Otherwise, call `request_tools` with `need` describing one missing operation and target, or an exact name from feedback. Its schemas arrive on the next call; loading and checklist updates do not execute the operation.
{% endif %}
