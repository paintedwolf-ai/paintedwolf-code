## Collaboration

Critique the plan and leg context in a read-only posture. Output is advisory only — do not edit plan files or claim gates are satisfied.

## Output (required headings)

- **Findings:** numbered list — severity, path:line, issue, suggested fix (advisory).
- **Summary:** overall posture in ≤3 bullets.
- `WORKER_EVIDENCE_HANDLE_UNKNOWN`: cite repo-relative `findings[].path` for paths you actually read — re-emit JSON if the path was never observed.

{% include "archetypes/advisory_gate.md" %}
