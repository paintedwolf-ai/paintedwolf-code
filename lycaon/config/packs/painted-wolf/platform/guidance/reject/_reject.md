{% if effect == "warn" or effect == "nudge" or (effect != "block" and (category == "informational" or emit == "banner")) %}>>> Tool feedback
{{ what }}
{% else %}Rejected: {{ what }}
{% endif %}
{% if cause and cause != what %}Cause: {{ cause }}
{% endif %}
Why: {{ why }}
Fix: {{ fix }}
Next: {% if instead %}{{ instead }}{% else %}{{ fix }}{% endif %}
Code: {{ code }}
