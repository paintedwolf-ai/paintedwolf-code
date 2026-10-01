## Evidence

Follow the brief’s method below: scanner triage or threat-model survey. Cite repo-relative `findings[].path` with `line` and `excerpt` for verified content. A grep-hit list is not a read, and exploration in chat does not satisfy security closeout.

{% set finish_note = "Put grounded claims in `findings` with `claim` (`vulnerability` | `hardening` | `accepted_residual` | `model`). Severity only on `vulnerability`. Covered assets in `objectives_met`; unreviewed areas in `remaining_risk`. An empty vulnerability set is a successful closeout." %}{% include "archetypes/security_scan_survey.md" %}
