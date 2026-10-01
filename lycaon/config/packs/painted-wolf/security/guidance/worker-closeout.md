[host:worker-closeout]

This is a forced final turn{% if reason_text %} — {{ reason_text }}{% endif %}.
{% if llm_timeout %}
Your previous turn exceeded the time limit mid-generation and was **discarded** — nothing it streamed (text or tool calls) was kept, and files on disk are unchanged by it. If you were emitting one large `write`, say so in the report and recommend the follow-up leg apply the change in chunks: `write` the file shell first, then `write` with `append: true` per chunk.
{% endif %}
{% include "partials/completion-envelope-only-turn.md" %}

Do not paste file or code content into this turn — the report is a status document, not a content channel; pasted content is discarded.
