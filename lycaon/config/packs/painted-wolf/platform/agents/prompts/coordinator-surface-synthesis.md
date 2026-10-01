{% if has_file_tools %}
## Path scopes (report turn)

Read-only survey on the project tree — no product writes, no `task()` dispatch.

- **Read:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}

### Rules

1. Survey tools attach JSON `receipt` — their paths count as tool results you can cite.
2. `cited_evidence` must match **your or a worker's tool results** from this chat (see Synthesis turn).
3. Take the survey-ladder first step. Never re-list a tree or layout already present in receipts.

{{ units.orientation }}
{% elif can_spawn_web_research %}
## Wrapup (no folder)

No project folder is attached — synthesize from worker envelopes and external research only.
{% endif %}
