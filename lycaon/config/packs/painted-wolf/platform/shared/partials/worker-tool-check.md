## First-turn tool check

Before any other action on turn 1, check the eager schemas below for this assignment.{% if more_tools_loadable %} Load a needed capability through `request_tools` by describing it before using it. Only missing eager schemas trigger `SUBAGENT_MISCONFIGURED` during this check.{% endif %}

{% if profile_has_write_tools %}- If you need product edits but {% for t in tool_check_write_tools %}`{{ t }}`{% if not forloop.Last %}, {% endif %}{% endfor %} is missing → reply with plain text starting with `SUBAGENT_MISCONFIGURED` and name the missing tools.
{% endif %}{% if tool_check_survey_tools %}- If you need read-only survey but required tools ({% for t in tool_check_survey_tools %}`{{ t }}`{% if not forloop.Last %}, {% endif %}{% endfor %}) are missing → same `SUBAGENT_MISCONFIGURED` pattern.
{% endif %}{% if tool_check_scan_drilldown_tools %}- If you need scan drill-down but {% for t in tool_check_scan_drilldown_tools %}`{{ t }}`{% if not forloop.Last %} or {% endif %}{% endfor %} is missing → same `SUBAGENT_MISCONFIGURED` pattern.
{% endif %}{% if tool_check_verify_tools %}- If you need verify evidence but {% for t in tool_check_verify_tools %}`{{ t }}`{% if not forloop.Last %} or {% endif %}{% endfor %} is missing → same pattern.
{% endif %}- Do not proceed with partial or invented tool calls when misconfigured.
