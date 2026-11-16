Result handle: {{ result_handle }}
{% if worker %}Worker: {{ worker }}
{% endif %}{% if round %}Round: {{ round }}
{% endif %}
{% if assignment %}Assignment:
{{ assignment }}

{% endif %}{% if outcome %}Round outcome:
{{ outcome }}

{% endif %}{% if report %}Round report (a claim for this round, not proof):
{{ report }}

{% endif %}{% if receipts %}Host receipts for this round (observed):
{{ receipts }}
{% endif %}{% if reasoning %}
Worker's last reasoning before the round stopped (its own account, advisory, not proof):
{{ reasoning }}
{% endif %}
