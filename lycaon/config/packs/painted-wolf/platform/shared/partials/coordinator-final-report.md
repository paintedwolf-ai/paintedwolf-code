### Completion reply

{% include "partials/coordinator-preflight.md" %}

Write the answer in user-facing Markdown, without tools, using the citation forms under Text and citations.

- Use exact observed paths{% if root_count > 1 %} with folder prefixes{% endif %}, including created or edited files and worker findings.{% if profile_has_summarize %} `summarize` source windows and anchors are citable; digests alone are not.{% endif %} Earlier observations in this chat remain citable; do not repeat reads just for citations.
{% if web_search_enabled %}- Link only URLs from `web_search` or `fetch_url`, preserving scheme and trailing slash.
{% endif %}{% if profile_has_render_view or profile_has_capture_page or profile_has_terminal_snapshot or profile_has_terminal_capture %}- `[artifact:<uuid>]` takes visual UUIDs from tool results or worker `proof_json.visual_artifact_ids`. Include the final interface capture. Evidence handles and Markdown or HTML UUID embeds do not present images (`PRESENT_MARKDOWN_EMBED`).
{% endif %}- For changed work, use `[verify: blocked - reason]` if primary interaction or launch could not be confirmed. Explain scope and outcome in prose. This assessment does not mint a host pass or waive a workflow gate.
- Only an enabled workflow report phase can request a report document. In every other phase, give an ordinary completion reply, and do not offer or generate a report document. The host controls download availability.

Use the user's terms. Unless asked, keep host mechanics, recovered denials, and test-tooling details out of progress updates and the completion reply. Name the observed product behavior, secondary untested paths, and any unresolved resource or required action.
`Code: COORDINATOR_CLOSEOUT_NO_ANSWER`
