## Covered source page
{% for msg in recent_messages %}- role={{ msg.role }} origin={{ msg.origin }} authority={{ msg.authority }} trust_tier={{ msg.trust_tier }}{% if msg.compaction_strategy %} compaction_strategy={{ msg.compaction_strategy }}{% endif %}{% if msg.evidence_handles %} evidence_handles={% for handle in msg.evidence_handles %}{% if not forloop.First %}, {% endif %}{{ handle }}{% endfor %}{% endif %}{% if msg.host_secret_redaction %} host_secret_redaction=true [host redacted secret values; sources unchanged by redaction]{% endif %}{% if msg.host_secret_references %} host_secret_references=true [host wrote protected values as references; sources still hold the values; copy references exactly]{% endif %}
  content: {{ msg.content }}
{% endfor %}

## Prior continuation
{{ prior_continuation }}
{% if current_request %}
## Current request (relevance only)
The host retains this request verbatim. Use it to select relevant observations; do not replace it with summary fields.
{{ current_request }}
{% endif %}
## Summary instructions
Generate a continuation record for the receiving agent.
- Merge the prior continuation with this page; retain constraints and unresolved work.
- Use the newest supported state. Distinguish completed effects, attempted actions, and pending verification; preserve failures still requiring recovery.
- Keep paths and usable receipt, process, or message references needed to resume or inspect results.
- Write one supported fact per item. Populate applicable arrays; leave an array empty only when the supplied evidence supports no entries.

## Output schema
Maximum items: facts 8, completed 5, pending 5, reacquire 8, constraints 5. Each item is at most 160 Unicode characters.
{"facts":[],"completed":[],"pending":[],"reacquire":[],"constraints":[]}
