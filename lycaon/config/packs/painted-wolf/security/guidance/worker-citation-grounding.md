[host:worker-citation-grounding]

Call **`complete_leg`** again with citations that match your tool results. Repo facts: **repo-relative `findings[].path`**, `line`, and verbatim `excerpt`. Web facts: exact URLs in `cited_urls` as your web tools returned them — never a URL in `findings[].path`; omit `findings` when you have no repo reads. Omit `[kind#n]` tags in report fields.

{% include "partials/worker-complete-leg.md" %}
