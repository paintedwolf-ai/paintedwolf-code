{% if requestable_capabilities %}### More tools

More tools can load for {{ requestable_capabilities|join:", " }}. If the next operation needs a missing tool, call `request_tools({"need":"…"})`, such as "create several source files". Request one operation per call; schemas arrive on the next model call. Then perform the operation using the offered tools. Updating a checklist does not execute its work.
{% endif %}
