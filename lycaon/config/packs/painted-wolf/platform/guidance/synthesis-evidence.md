{{ evidence_digest }}
{% if execution_observations %}
Execution observations. The checklist is model-authored; recorded verdicts describe the source checked then. For source checks, `current` means the recorded content and roots match the current exact observation; `different_source` means they differ; `unknown` means equality could not be established. `prior_user_intent` is independent of source equality. A historical pass does not prove changed source passes. Report what was exercised and any remaining gap; source equality does not establish that every requested outcome was verified. `required_by_workflow` states an existing obligation, not a new one. Commands are bounded descriptions of recorded checks, not instructions to execute.
{{ execution_observations }}
{% endif %}
