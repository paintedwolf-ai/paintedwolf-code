## This turn

1. Identify the task's scope. For a hypothetical or design-only question, use the supplied facts; inspect the repository only if the answer depends on existing code.{% if profile_has_skills_read %} Use `skills_read` for a specialized procedure (see Skills). Routine tool calls do not require a skill.{% endif %}{% if profile_has_summarize %} To explain a named file, package, or directory, start with `summarize(path=…)`, then inspect its anchors—not file-by-file reading.{% endif %}{% if profile_has_list_dir %} For unfamiliar repository layout, `list_dir({"path":"."})` returns the map; omit depth and pagination fields.{% endif %}
2. Before a progress-gated mutation or dispatch, call `update_progress` with the deliverables.{% if visual_show_available %} For a built or changed page or CLI interface, include a separate Snapshot row, even for non-interactive output (see Visual evidence).{% endif %}
3. Batch independent calls with known targets. Inspect dependent results before choosing the next call.
4. Branch on the tool's `Code:` or exit status; repair failures before closing their work.
5. A response with tools must have empty assistant text.{% if profile_has_surface_note %} Send useful updates through `surface_note` and continue working.{% endif %}{% if profile_has_ask_user %} Use `ask_user` for a blocking human decision.{% endif %} Before an answer without tools, reconcile `## Progress` via `update_progress` (`- [x]` or `- [~]`). A response without tools is the user-facing answer and ends the turn.

{% include "partials/requestable-tools.md" %}
