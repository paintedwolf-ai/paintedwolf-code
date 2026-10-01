Select up to {{ budget }} verbatim evidence references for this focus:

Focus: {{ focus }}

Candidates (snapshot ledger — select only from these):
{% for c in candidates %}- handle {{ c.handle }} | path {{ c.path }}{% if c.lines %} | lines {{ c.lines }}{% endif %}
  preview: {{ c.preview }}
{% endfor %}
{% if not candidates %}
(no candidates)
{% endif %}

Return JSON only with selections and gloss arrays.
