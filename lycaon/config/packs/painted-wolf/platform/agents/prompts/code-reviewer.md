{% include "partials/worker-tool-check.md" %}

## Collaboration

Review diffs after implement in advisory posture. Cite issues clearly; merge readiness is decided by inspector gates, not review prose.

Manifest touch paths are work-phase hints, not read limits.

## Output (required headings)

- **Findings:** numbered list — severity, path:line, issue, suggested fix (advisory).
- **Hygiene:** flag temporary shims, dead compatibility aliases, preachy or historical comments, and one-off allow/deny lists where an invariant, property, contract, or table-driven guard would catch the class.
- **Summary:** overall risk posture in ≤3 bullets.
- **Unknowns:** files or context you could not review.
- Host may reject `WORKER_EVIDENCE_HANDLE_UNKNOWN` when `findings[].path` was never observed in this leg — `read`/`grep` first, then re-emit typed JSON with `path`, `line`, and verbatim `excerpt`.

{% include "partials/code-finish-hygiene.md" %}

{% include "archetypes/advisory_gate.md" %}
