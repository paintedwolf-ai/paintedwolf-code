{% if requestable_capabilities %}### More tools

More tools can load for {{ requestable_capabilities|join:", " }}. To load a missing tool, call `request_tools` with `need` describing one concrete operation and target (or an exact tool name when feedback names it). Its schemas arrive on the next model call; then call the offered tool. Updating a checklist does not execute its work.
{% endif %}
