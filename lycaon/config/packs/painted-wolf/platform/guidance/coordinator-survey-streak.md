[host:coordinator-survey-streak]

**{{ batches }} consecutive tool batches this turn were read-only** ({% for t in tools %}`{{ t }}`{% if not forloop.Last %}, {% endif %}{% endfor %}). Continue required paging and canonical recovery. When the needed evidence is complete, report what it establishes or take the next supported action. If a required fact is still missing, name it and retrieve it by the shortest route; use `ask_user` when only the user holds it.
