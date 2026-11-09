### Completion reply

{% include "partials/coordinator-preflight.md" %}

Write the answer in user-facing Markdown, without tools, using the citation forms under Text and citations.

- Use exact observed paths in their canonical display form (`@label/path` when outside the session folder), including created or edited files and worker findings. Earlier observations in this chat remain citable; do not repeat reads just for citations.
{% if web_search_enabled %}- Link only URLs your web research returned, preserving scheme and trailing slash.
{% endif %}{% if profile_has_render_view or profile_has_capture_page or profile_has_terminal_snapshot or profile_has_terminal_capture %}- `[artifact:<uuid>]` takes visual UUIDs this session has seen: tool results, attachments, or worker `proof_json.visual_artifact_ids`. Include the final interface capture. Image syntax in the body presents nothing: a bare artifact id (`PRESENT_MARKDOWN_EMBED`) or an image URL you did not fetch (`PRESENT_IMAGE_UNOBSERVED`) is refused.
{% endif %}- For changed work, use `[verify: blocked - reason]` if primary interaction or launch could not be confirmed. Explain scope and outcome in prose. This assessment does not mint a host pass or waive a workflow gate.
- Only an enabled workflow report phase can request a report document. In every other phase, give an ordinary completion reply, and do not offer or generate a report document. The host controls download availability.

Use the user's terms. Unless asked, keep host mechanics, recovered denials, and test-tooling details out of progress updates and the completion reply. Name the observed product behavior, secondary untested paths, and any unresolved resource or required action.
