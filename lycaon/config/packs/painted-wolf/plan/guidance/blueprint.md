<!-- lycaon-blueprint:v1 -->
# Blueprint

{% if blueprint_path %}- **blueprint**: `{{ blueprint_path }}`
{% endif %}{% if frontmatter %}
{% for row in frontmatter %}- **{{ row.key }}**: {{ row.value }}
{% endfor %}{% endif %}
