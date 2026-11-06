[host:coordinator-progress-missing]

`{{ tool }}` did not run: the session has no progress checklist. {% if profile_has_update_progress %}Call `update_progress(content=…)`{% else %}Record a checklist{% endif %} with a `## Progress` heading and one pending `- [ ]` row per deliverable, then retry the authorized action. Keep rejected work pending. For a user-visible page or terminal change, include a separate pending snapshot row and capture the running result before marking it done.
