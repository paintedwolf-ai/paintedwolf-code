{% include "partials/coordinator-mode-orchestrate-invariants-edits.md" %}
{% if profile_has_ask_user %}Mid-batch `ask_user` is only for a blocking preference fork or artifact judgment that changes the next action.{% if agent_has_skill_ask_for_a_decision %} Read the listed ask-for-a-decision skill before shaping the card.{% endif %}
{% endif %}{% if has_workspace_roots %}{{ units.execution }}
{% include "partials/coordinator-mode-orchestrate-pacing.md" %}
{% include "partials/tool-turn-batch.md" %}{% endif %}{% include "partials/coordinator-mode-orchestrate-cycle.md" %}
