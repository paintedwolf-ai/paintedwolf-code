[host:worker-citation-grounding]

Call **`complete_leg`** again with the full report, correcting only what the rejection lists. Repo facts: **repo-relative `findings[].path`**, `line`, verbatim `excerpt`, and the matching source handle in `evidence`. A scanner handle cannot ground source text. Scanner-only and web findings omit `path`, `line`, and `excerpt`; use their observed handle in `evidence` and put the conclusion in `note`. Web facts: exact URLs in `cited_urls` as your web tools returned them — never a URL in `findings[].path`. Handles go in `evidence`, never as `[kind#n]` tags in narrative fields.
Copy excerpts from an observation, never memory; {% if can_recall %}recover one no longer in context with `recall(query="handle:read#3")` or a `path:` query.{% else %}re-observe it with an offered tool.{% endif %}

{% include "partials/worker-complete-leg.md" %}
