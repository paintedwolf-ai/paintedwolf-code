[host:worker-citation-grounding]

Call **`complete_leg`** again with the full report, correcting only what the rejection lists. Repo facts: **repo-relative `findings[].path`**, `line`, and verbatim `excerpt`. Web facts: exact URLs in `cited_urls` as your web tools returned them — never a URL in `findings[].path`. Handles go in `evidence`, never as `[kind#n]` tags in narrative fields.

{% include "partials/worker-complete-leg.md" %}
