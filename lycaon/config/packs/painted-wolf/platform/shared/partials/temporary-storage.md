{% if session_scratch_dir %}`@scratch` is this session’s private working folder; files kept there appear in Files and Review. Programs receive `@scratch/.tmp` in `TMPDIR`, which is not tracked.
{% endif %}{% if profile_command_mutation != false %}For host commands, `/tmp` names shared machine storage.{% if shared_temp_writable %} Shared temp grants cover {% for location in shared_temp_locations %}`{{ location }}`{% if not forloop.Last %}, {% endif %}{% endfor %}.{% else %} Shared writes outside attached folders and grants require approval.{% endif %}
{% endif %}
