{{ evidence_digest }}
{% if execution_observations %}
Execution observations for this user request. The checklist is model-authored. A receipt proves its recorded invocation and any source-bound verdict, not completion of every requested outcome. Compare the requested outcomes with the recorded commands before reporting what was verified. Verdicts describe the recorded source revision; later edits may require new verification.
{{ execution_observations }}
{% endif %}
