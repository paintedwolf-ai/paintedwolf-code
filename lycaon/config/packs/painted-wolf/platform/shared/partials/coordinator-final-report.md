### Completion reply

{% include "partials/coordinator-preflight.md" %}

Write the answer in normal user-facing Markdown, without tools. End with one trailing `json` fence holding an object with the fields below; the host hides it, and with no fields, omit it. Never send a JSON-only answer.

- `cited_evidence`: objects with `{"evidence":"tool#n"}` or `path` plus optional `line`/`excerpt`. Use exact observed paths{% if root_count > 1 %} with folder prefixes{% endif %}, including created/edited files and worker findings.{% if profile_has_summarize %} `summarize` source windows and anchors are citable; digests alone are not.{% endif %} Earlier observations in this chat remain citable; do not repeat reads just for citations. Explain code with inline `path:line` links.
{% if web_search_enabled %}- `cited_urls`: exact URLs from `web_search`/`fetch_url`, preserving scheme and trailing slash; never put URLs in `cited_evidence`.
{% else %}- `cited_urls`: `[]`.
{% endif %}{% if profile_has_render_view or profile_has_capture_page or profile_has_terminal_snapshot or profile_has_terminal_capture %}- `artifact_ids`: visual UUIDs from tool results or worker `proof_json.visual_artifact_ids`. Include the final interface capture here. This field presents images; evidence handles and Markdown/HTML UUID embeds do not (`PRESENT_MARKDOWN_EMBED`).
{% endif %}- For changed work, include `verification` with one method: `inspection`, `targeted`, `project`, or `blocked`, and your actual reason. Use `blocked` if primary interaction or launch could not be confirmed. Explain scope and outcome in prose. This assessment does not mint a host pass or waive a workflow gate.
- Only an enabled workflow report phase can request a report document. In every other phase, give an ordinary completion reply without document report fields, and do not offer or generate a report document. The host controls download availability.

Use the user's terms. Unless asked, keep host mechanics, recovered denials, and test-tooling details out of progress updates and the completion reply. Name the observed product behavior, secondary untested paths, and any unresolved resource or required action.
`Code: COORDINATOR_CLOSEOUT_ENVELOPE_ONLY`
