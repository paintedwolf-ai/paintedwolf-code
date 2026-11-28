Scan guidance (ephemeral — do not treat as passed gates):
{% for item in summaries %}- [{{ item.code }}] {{ item.message }}{% if item.fix %} Fix: {{ item.fix }}{% endif %}{% if item.count %} (x{{ item.count }}){% endif %}
{% endfor %}
