## Progress checklist

**Author before mutate/dispatch.**

Before `write`, `edit`, `replace_lines`, `restore_version`, `task`, or `delegate_*`, call `update_progress` with a `## Progress` checklist. `delete` and explanation-only work do not require it. Name deliverables from the request and known facts; survey only when needed to identify them.

One flat `- [ ]` row per deliverable; a small leaf needs one row. Limits: {{ max_author_progress_lines }} lines and {{ max_progress_label_chars }} characters per label (long labels are trimmed). Close with `- [x]`; `- [~]` means terminal will-not-pursue, never in-progress. Notes use `- [>]`. Update only when scope or status changes; `unchanged` is success. Open rows block synthesis.

{% include "partials/coordinator-progress-closure.md" %}
{% include "partials/coordinator-trust-bundle-tiers.md" %}
