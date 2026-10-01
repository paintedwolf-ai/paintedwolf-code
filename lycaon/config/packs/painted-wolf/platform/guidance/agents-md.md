{% if mode == "index" %}
## Available AGENTS.md files

{% for item in index %}- `{{ item.path }}`
{% endfor %}
The guidance chain for any path you touch is injected automatically — this index is orientation, not a reading list.
{% elif mode == "chain" %}
## Project agent guidance (applicable to `{{ target_path }}`)

{% for entry in chain %}{% if entry.content %}
<!-- from {{ entry.path }} -->
{{ entry.content }}
{% endif %}{% endfor %}{% endif %}
