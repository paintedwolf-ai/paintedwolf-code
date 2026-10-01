{% if agent_skills %}{{ skills_heading|default:"##" }} Skills

Skills provide task procedures. When needed or requested, call `skills_read({"need":"describe the procedure you need"})` before acting. Describe the next procedure and its target (e.g. "review keyboard navigation in a dialog"), not the overall project goal. Check that the selected skill fits; an unrelated match does not change the task. Refine an unrelated lookup with a more specific procedure or continue without a skill. Never repeat an unchanged lookup or read skills speculatively. Reuse preloaded or previously read instructions. Read listed resources with `skills_read({"need":"selected-skill-name","resource":"relative/path"})` before following them. Host policy and user scope still apply.
{% endif %}
