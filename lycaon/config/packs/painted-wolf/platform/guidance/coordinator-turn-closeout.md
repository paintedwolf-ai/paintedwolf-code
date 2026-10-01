[host:coordinator-turn-closeout]

This is your final turn — {{ reason_text }}, so no tools are available now.
{% if llm_timeout %}
Your previous turn was discarded mid-generation — nothing it streamed was kept and files on disk are unchanged by it. If it was one large `write`, a later turn should redo the change in chunks (`write` the shell, then `write` with `append: true` per chunk).
{% endif %}
Give a closing summary of delivered work and unresolved limits. No new work or file dumps. Blocked validation is not a pass and does not clear workflow gates.
