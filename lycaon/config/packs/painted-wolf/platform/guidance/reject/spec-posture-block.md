>>> Spec posture blocked
Tool: {{ tool }}
{% if phase_required %}Blocked at: Phase {{ phase_required }}{% if phase_name %} — {{ phase_name }}{% endif %}
{% endif %}{% if what %}What: {{ what }}
{% endif %}{% if cause %}Cause: {{ cause }}
{% endif %}{% if why %}Why: {{ why }}
{% endif %}{% if fix %}Fix: {{ fix }}
{% endif %}{% if instead %}Instead: {{ instead }}
{% endif %}{% if required %}Required action: {{ required }}{% endif %}
Progress: {{ progress }}
Code: {{ code }}{% if details %}

Details:
{{ details }}{% endif %}
