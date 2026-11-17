{% if worker %}Worker `{{ worker }}`{% else %}A waiting worker{% endif %} is waiting on {% if producer %}worker `{{ producer }}`{% else %}a worker{% endif %}, which {% if producer_state == "closed" %}was closed{% elif producer_state == "rejected" %}had its changes rejected with no round after{% else %}failed{% endif %}, so it cannot start. It keeps waiting until you act.

Start a replacement for what it needed{% if profile_has_worker_cancel %}, or withdraw it with `worker_cancel`{% endif %}.
