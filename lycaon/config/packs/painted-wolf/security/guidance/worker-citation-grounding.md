[host:worker-citation-grounding]

Call **`complete_leg`** again with the full report, fixing what the rejection lists. Repo facts: **canonical `findings[].path`** (bare in the session folder, `@label/path` elsewhere), `line`, verbatim `excerpt`, with the source handle in `evidence`. A scanner handle cannot ground source text. Scanner-only and web findings omit `path`, `line`, `excerpt`; use their observed handle in `evidence`, conclusion in `note`. Web facts: exact URLs in `cited_urls` — never a URL in `findings[].path`. Handles go in `evidence`, never as `[kind#n]` tags in narrative.
Copy excerpts from an observation, never memory; {% if can_recall %}recover one with `recall(query="handle:read#3")` or a `path:` query.{% else %}re-observe it with an offered tool.{% endif %}

{% include "partials/worker-complete-leg.md" %}
