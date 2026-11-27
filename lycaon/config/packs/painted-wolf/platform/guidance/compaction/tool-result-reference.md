[compacted {{ tool }} result{% for handle in evidence_handles %}; {{ handle }}{% endfor %}; outcome={{ outcome }}]
{% if can_recall_history %}Observation body compacted. Recover the recorded content with `recall(history={{ history_json|safe }})`.{% else %}Observation body compacted; reacquire needed details with the reads offered on this turn.{% endif %}
