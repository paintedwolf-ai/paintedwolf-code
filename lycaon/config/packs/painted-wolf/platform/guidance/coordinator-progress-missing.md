[host:coordinator-progress-missing]

`{{ tool }}` did not run: the session has no progress rows. {% if profile_has_update_progress %}Call `update_progress(replace=[{"label":"…"}])`{% else %}Record progress deliverables{% endif %} with one pending row per deliverable, then retry the authorized action. Keep rejected work pending. For a user-visible page or terminal change, include a separate pending snapshot row and capture the running result before marking it done.
