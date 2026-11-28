[host:coordinator-turn-closeout]

This is your final turn — {{ reason_text }}, so no tools are available now.
{% if llm_timeout %}
Your previous turn was discarded mid-generation — nothing it streamed was kept and files on disk are unchanged by it.{% if session_command_mutation_capable %} If it was one large command redirect, a later turn should redo the change in chunks (create the file with a staged command redirect, then append bounded chunks).{% else %} Summarize what earlier turns already established and note any interrupted inspection or draft for the next turn.{% endif %}
{% endif %}
Give a closing summary of delivered work and unresolved limits. {% include "partials/coordinator-forced-final-answer.md" %}
{% include "partials/project-path-presentation.md" %}

No new work or file dumps. Blocked validation is not a pass and does not clear workflow gates.
