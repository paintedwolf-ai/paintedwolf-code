Search query (answer this exactly): {{ query }}
Return {{ min_leads }}-{{ max_leads }} leads (title+host), up to {{ max_seeds }} extra seed roots not covered by a lead, {{ expand_min }}-{{ expand_max }} expand phrases, and fresh.
The host will verify up to {{ hit_target }} hits from these publisher roots — return enough distinct authoritative hosts (docs, reference, news, tutorials, primary sources) to support that breadth.
If the subject may be newer than your training data, recall stable publisher hosts and topic-shaped headline terms for the live crawler to test. This stage supplies candidates, not facts: preserve breadth without claiming an article exists or inventing a URL path.
{% if period_current %}Window: current — rank coverage of today's state as of the date line, not obsolete material. A year appearing in the query text is part of the subject; it does not narrow the window.
{% else %}Window: {{ period }} — the caller asked about that period specifically. Rank coverage published in or about it, and set fresh to false: newer material does not answer this question.
{% endif %}{% if task_hint %}User request (stay on this subject; do not drift to adjacent topics):
{{ task_hint }}
{% endif %}
