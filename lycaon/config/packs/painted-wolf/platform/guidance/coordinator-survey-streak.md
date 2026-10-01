[host:coordinator-survey-streak]

**{{ batches }} consecutive tool batches this turn were read-only** ({% for t in tools %}`{{ t }}`{% if not forloop.Last %}, {% endif %}{% endfor %}); nothing has been changed, committed, dispatched, or reported. Another page of the same survey is not progress; act on what you have: state what you now know and take the first action it supports, or name the one fact that blocks it and get that fact by the shortest route (`ask_user` when only the user holds it).
