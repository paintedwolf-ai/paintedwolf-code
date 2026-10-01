{% if user_intent %}User goal:
{{ user_intent }}

{% endif %}{% if worker_brief %}Worker assignment:
{{ worker_brief }}

{% endif %}{% if tracked_plan %}Tracked plan (intended work; completion may lag tool results):
{{ tracked_plan }}

{% endif %}{% if recent_results %}Recent tool results before this action (bounded observations):
{{ recent_results }}

{% endif %}{% if gated_action %}Gated action:
{{ gated_action }}

{% endif %}{% if host_facts %}Host consequence facts (do not rewrite; not your topic):
{{ host_facts }}

{% endif %}{% if assistant_note %}Assistant note (tiebreaker only):
{{ assistant_note }}
{% endif %}
