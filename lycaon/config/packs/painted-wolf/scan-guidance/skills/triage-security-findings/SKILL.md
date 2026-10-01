---
name: triage-security-findings
description: Use stored scanner evidence to assess findings, credibility, and changes introduced or resolved by a fix.
# Triage reads stored scans; running one is the coordinator's action.
optional_tools:
  - scan_pack
---

# Triage security findings

1. Use the exact workflow `scan_ids` when supplied; do not call `scan_list` or substitute a newer ambient scan. Otherwise resolve full ids with `scan_list` or `scan_pack`’s `per_scan` rows. Board assessment ids are orientation. Report unavailable bound scans; never guess ids.
2. Call `scan_summary` before drilling. If the scan is incomplete, follow its status or structured rejection instead of treating partial or absent ingest as a clean result.
3. Use `scan_query` as the primary finding source. Narrow by the requested category, rule, severity, or path and page with `offset` whenever `truncated` is true.
4. Inspect source only at paths returned by the scanner. Use bounded `read`, text `grep`, or `summarize` to decide whether the flagged data and control path are reachable; do not replace scanner triage with a repository-wide manual audit.
5. Triage at category and rule level. Inspect up to three representative findings per requested category unless contradictory evidence or an explicit exhaustive request requires more. Separate confirmed positives from scanner noise and name unreviewed groups.
6. Once a post-fix scan exists, use `scan_compare` for introduced and resolved findings. Prefer the host's comparison buckets over manually diffing two `scan_query` result sets. If no later scan has landed yet, say so and stop — asking for one is the coordinator's call, not a finding.
7. Cite scan evidence and exact source locations. Report failed, running, truncated, or unavailable scans as limitations; none means clean only when a complete scan establishes it.

See [scan triage routing](references/scan-triage-routing.md) for ids, pagination, comparison, and stopping rules.
