{% if requestable_capabilities %}### More tools

Only a starting set of tools is offered. More tools can load in this turn for {{ requestable_capabilities|join:", " }}. To load a missing tool, call `request_tools` with `need` describing one concrete operation and target (such as creating a file, editing code, or running a command), or an exact tool name when feedback names it. Its schemas arrive on the next model call; then perform the operation using the offered tools. Updating a checklist does not execute its work.
{% endif %}
