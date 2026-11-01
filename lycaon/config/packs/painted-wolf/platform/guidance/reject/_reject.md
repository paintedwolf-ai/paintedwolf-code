{% if effect == "warn" or effect == "nudge" or (effect != "block" and (category == "informational" or emit == "banner")) %}>>> Tool feedback
{{ what }}
{% else %}Rejected: {{ what }}
{% endif %}
{% if execution.not_started %}State: Nothing executed. No files changed.
{% elif execution.active %}State: Program is not finished. Operations started: {{ execution.started }}. Completed effects: {{ execution.completed }}. Uncertain effects: {{ execution.uncertain }}. Inspect its jobs before starting replacement work.
{% elif execution.observed %}State: Operations started: {{ execution.started }}. Completed effects: {{ execution.completed }}. Uncertain effects: {{ execution.uncertain }}.
{% endif %}
{% if cause and cause != what %}Cause: {{ cause }}
{% endif %}
{% if why and why != cause and why != fix %}Why: {{ why }}
{% endif %}Fix: {{ fix }}
{% if instead and instead != fix %}Next: {{ instead }}
{% endif %}Code: {{ code }}
