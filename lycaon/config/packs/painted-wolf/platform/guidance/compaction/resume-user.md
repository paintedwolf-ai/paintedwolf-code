Generate a continuation record for the receiving agent.

## Summary instructions
- Synthesize the prior continuation and this source page. Retain relevant constraints and unfinished work.
- Conversation entries appear oldest to newest. When a later entry establishes a changed state, record the newest supported state and omit the earlier superseded state from current facts.
- Preserve file paths and recovery handles or message IDs for details that must be reacquired.
- Write one short supported fact per item. Include at least one fact; other arrays may be empty.

{% if current_request %}## Current request (relevance only)
The host retains this request verbatim. Use it to select relevant observations; do not replace it with summary fields.
{{ current_request }}

{% endif %}## Prior continuation
{{ prior_continuation }}

## Covered source page
{% for msg in recent_messages %}- role={{ msg.role }} origin={{ msg.origin }} authority={{ msg.authority }} trust_tier={{ msg.trust_tier }}{% if msg.compaction_strategy %} compaction_strategy={{ msg.compaction_strategy }}{% endif %}{% if msg.evidence_handles %} evidence_handles={{ msg.evidence_handles }}{% endif %}{% if msg.host_secret_redaction %} host_secret_redaction=true [host redacted secret values; sources unchanged by redaction]{% endif %}{% if msg.host_secret_references %} host_secret_references=true [host wrote protected values as references; sources still hold the values; copy references exactly]{% endif %}
  content: {{ msg.content }}
{% endfor %}

## Output schema
Maximum items: facts 8, completed 5, pending 5, reacquire 8, constraints 5. Each item is at most 160 Unicode characters.
{"facts":[],"completed":[],"pending":[],"reacquire":[],"constraints":[]}
