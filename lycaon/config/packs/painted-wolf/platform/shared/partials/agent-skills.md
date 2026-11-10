{% if agent_skills %}{{ skills_heading|default:"##" }} Skills

{% if full_skill_catalog %}Available procedures are listed below. Read an applicable skill with `skills_read({"need":"exact-skill-name"})` before following it. If none applies, continue with the task. Reuse instructions already read; open listed resources with `skills_read({"need":"exact-skill-name","resource":"relative/path"})`. Host policy and user scope still apply.
{% for skill in agent_skills %}
- `{{ skill.name }}`: {{ skill.description }}{% endfor %}
{% else %}Skills provide task procedures. When needed or requested, call `skills_read({"need":"describe the procedure you need"})` before acting. Describe the next procedure and its target (e.g. "review keyboard navigation in a dialog"), not the overall project goal. Check that the selected skill fits; an unrelated match does not change the task. Refine an unrelated lookup with a more specific procedure or continue without a skill. Never repeat an unchanged lookup or read skills speculatively. Reuse preloaded or previously read instructions. Read listed resources with `skills_read({"need":"selected-skill-name","resource":"relative/path"})` before following them. Host policy and user scope still apply.
{% endif %}{% endif %}
