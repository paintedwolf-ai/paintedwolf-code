{% if verify_command %}
The selected project check is `{{ verify_command }}`.
{% if profile_has_verify %}Bare `verify()` runs it.{% elif profile_has_command %}Run it with `command(command: "{{ verify_command }}", verification: true)` when project coverage is needed.{% endif %}
{% if profile_has_command %}Use `command(verification: true)` for targeted checks. The same selected check run through an offered execution tool can satisfy the project gate; a different command cannot.{% elif profile_has_verify %}This profile offers only the selected check for execution. Use inspection when sufficient, or report the coverage limitation; do not substitute a different command into `verify`.{% else %}No execution tool is offered. Inspect what you can and report any missing coverage.{% endif %}
`declared_command_match` identifies the command, not its success.
{% else %}
No project check is selected.
{% if profile_has_verify %}When executable validation is useful, discover the documented check appropriate to the task and run `verify(command: "…")`. Bare `verify()` cannot choose a command for you.{% endif %}
{% if profile_has_command %}Use `command(verification: true)` to identify a documented, task-appropriate check; ordinary commands are not test evidence.{% endif %}
{% if profile_has_command or profile_has_verify %}A settled passing check is valid evidence without a Settings change.{% else %}No execution tool is offered. Inspect what you can and report any missing coverage.{% endif %}
{% endif %}
`verification.method` reports scope: `project` for the selected or documented project check, `targeted` for a narrower check, and `inspection` for read-back without executable checks. Use `blocked` when required validation could not execute or finish; passing narrower checks does not remove that limitation. Describe actual outcomes and limits in `reason`.
