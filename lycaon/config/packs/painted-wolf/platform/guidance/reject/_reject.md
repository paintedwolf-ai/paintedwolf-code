{% if effect == "warn" or effect == "nudge" or (effect != "block" and (category == "informational" or emit == "banner")) %}>>> Tool feedback
{{ what }}
{% else %}Rejected: {{ what }}
{% endif %}
{% if execution.not_started %}State: No command operation ran or changed files.
{% elif execution.active %}State: Program is not finished. Operations started: {{ execution.started }}. Completed effects: {{ execution.completed }}. Uncertain effects: {{ execution.uncertain }}. Inspect its jobs before starting replacement work.
{% elif execution.observed %}State: Operations started: {{ execution.started }}. Completed effects: {{ execution.completed }}. Uncertain effects: {{ execution.uncertain }}.
{% endif %}
{% if cause and cause != what %}Cause: {{ cause }}
{% endif %}
{% if why and why != cause and why != fix %}Why: {{ why }}
{% endif %}{% if fix %}Fix: {{ fix }}
{% endif %}{% if instead and instead != fix %}Next: {{ instead }}
{% endif %}Code: {{ code }}
