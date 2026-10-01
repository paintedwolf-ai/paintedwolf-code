{% if has_file_tools %}
For an unresolved product fork before the first writer, use `ask_user` rather than assistant prose; do not ask facts tools can resolve, and do not ask the user to invent names they left unspecified.{% if agent_has_skill_ask_for_a_decision %} Read the listed ask-for-a-decision skill before shaping the card.{% endif %}
Keep diagnosed small edits, named deletions, and live browser/terminal debugging inline.{% if more_tools_loadable %} Capabilities not on this call load through `request_tools` by describing the need in plain words; they arrive on the next call.{% endif %}
{{ units.execution }}
{% include "partials/coordinator-mode-investigate-progress.md" %}
{% include "partials/tool-turn-batch.md" %}{% endif %}
