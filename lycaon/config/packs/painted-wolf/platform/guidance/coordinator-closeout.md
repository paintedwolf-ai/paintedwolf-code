[host:coordinator-closeout]

This is a forced final turn{% if reason_text %} — {{ reason_text }}{% endif %}.
{% if llm_timeout %}
The timed-out turn was discarded: no text or tool calls were kept, and it did not change files. If it was composing a large write, report that and recommend smaller write chunks.
{% endif %}
Stop calling tools and give your final answer. {% include "partials/coordinator-forced-final-answer.md" %}
If a report document is requested, end with its one `json` fence; never send JSON alone.

{% include "partials/project-path-presentation.md" %}

Do not paste file or code content; it is discarded.
