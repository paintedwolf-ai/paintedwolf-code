---
name: survey-a-threat-model
description: Map a system’s threat model from source and classify claims against it; not scanner-finding triage.
# record_finding is the worker's half; the coordinator reads the result.
optional_tools:
  - record_finding
---

# Survey a threat model

1. Orient from the survey ladder: named scope → `summarize(path=…)`; no destination and a matching bundle → `survey_repo` once. When the host supplies workflow scan ids, use exactly those with `scan_summary` / `scan_query`; never replace them with project-ambient rows from `scan_list`.
2. Infer or restate the **target** threat model from this repo: system kind, who can reach it, how authentication works. Read `SECURITY.md` / deploy docs when present. Do not assume a public web app. Record the model as `claim: model` findings (facts, not issues).
3. Name assets (secrets, identity, data stores), entry points (HTTP, sockets, CLI, workers), and trust boundaries that matter **under that model**.
4. Treat every intended control as a hypothesis. Trace the reachable path from entry point through parsing, defaults, authentication/authorization, and the effectful sink. Inspect constructors, alternate callers, error paths, and bypass routes. A helper name, comment, or architecture description is not enforcement; identify the physical boundary and prove callers reach it. Cite `path:line` with a short excerpt.
5. Classify each issue finding before any severity band:
   - `vulnerability` — exploitable by an in-scope adversary; include `adversary`, `precondition`, and `severity` (`high` / `medium` / `low`)
   - `hardening` — real improvement; not exploitable under the stated model; omit `severity`
   - `accepted_residual` — only if this repo documented the residual; omit `severity`
6. Adjudicate scan warnings from source without pre-classifying them as noise. Record only claims you grounded. Unreviewed areas and incomplete bound scans go in `remaining_risk`, not as issues. An empty vulnerability set is a successful closeout.
7. Finish with citation triples plus the optional `claim` / `adversary` / `precondition` / `severity` fields. Do not treat the Findings plane (`record_finding` peer notes) as the closeout.
