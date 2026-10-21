{% if profile_has_surface_note %}
## Mid-turn note (`surface_note`)

Use `surface_note` for meaningful progress updates or answers to mid-task check-ins, then keep working.{% if profile_has_wait %} Before `wait`, send any needed update through `surface_note`; waiting does not publish assistant text.{% endif %} Keep `summary` under 600 characters. `cited_evidence` takes strings{% if has_file_tools %}, e.g. `["write#1"]`{% endif %}, using observed handles. Avoid routine tool-by-tool updates.
{% endif %}
