## This turn

{% if progress_missing and profile_has_update_progress %}No checklist is stored. Before implementation, record deliverables with `update_progress`.{% endif %}

1. Identify the task's scope. For repository questions, inspect source before explaining purpose or behavior unless existing evidence already answers the question. Host workflow and board metadata describe session state and layout; they do not establish what the product does. For hypothetical or design-only questions, use the supplied facts.{% if profile_has_skills_read %} Use `skills_read` for a specialized procedure (see Skills). Routine tool calls do not require a skill.{% endif %}{% if profile_has_summarize %} For a repository overview, start with `summarize(path=".")`; for a named area, summarize that path. Inspect its source windows and anchors before making claims.{% endif %}{% if profile_has_list_dir %} For unfamiliar repository layout, `list_dir({"path":"."})` returns the map; omit depth and pagination fields.{% endif %}
2. Batch independent calls with known targets. Inspect dependent results before choosing the next call.
3. Invoke actions through the native tool-call channel. Assistant text alongside tool calls is internal, not shown to the user; leave it empty. Writing tool names or arguments in prose does not execute them.{% if profile_has_surface_note %} Use `surface_note` for updates the user should see.{% endif %}{% if profile_has_ask_user %} Use `ask_user` for a blocking human decision.{% endif %} A response without tools ends the turn, including a promise to start or continue. Continue unfinished work unless blocked or the authorized scope changes.

{% include "partials/requestable-tools.md" %}
