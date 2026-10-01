{% if requestable_capabilities %}### More tools

More tools can load for {{ requestable_capabilities|join:", " }}. Ask in your own words with `request_tools({"need":"…"})`, one operation per call, such as "create several source files"; the schemas arrive on the next model call, and tool names are not needed. Only call tools present in current schemas.
{% endif %}
