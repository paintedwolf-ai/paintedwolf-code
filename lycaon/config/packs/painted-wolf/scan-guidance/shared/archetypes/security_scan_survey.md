{% include "partials/coordination-loop.md" %}

## Follow the assigned review method

**Threat-model survey:** name assets, entry points, trust boundaries, and authorization. Start with `summarize` for named scope, or `survey_repo` for a matching bundle when no destination is known. Trace intended controls through defaults, alternate callers, failures, and effectful sinks. A helper name or comment is not proof of enforcement. Scanner absence or zero findings does not end this review. Do not open with unbounded `list_dir` / `grep`.

**Scanner triage:** follow this procedure before native discovery:

1. If the host supplies workflow **`scan_ids`**, pass that exact array to `scan_summary` and `scan_query`; never substitute an ambient scan. Otherwise resolve full ids with `scan_list` (omit `project_dir`) or `scan_pack`’s `per_scan[].scan_id`. A board assessment token is not a scan id. If a bound id is unavailable, report it; for an unbound `SCAN_NOT_FOUND`, refresh `scan_list` once.
2. **`scan_summary`** for status, then **`scan_query`** for findings. Paginate with `offset` when `truncated`; inspect source at the returned paths to adjudicate warnings.
3. **`scan_compare`** after a fix: use assigned pre/post-fix ids. Only for an unbound review, resolve the latest `new_scan_id` and omit `old_scan_id` for the previous compatible authoritative run. Board regression tails are orientation, not change evidence; cite the comparison artifact.

**Branch on scan status:**

- `complete` with findings: drill down and assess them from source.
- `complete` with zero findings: report the empty result and its scope; do not invent issues.
- `pending` / `running`: re-check the same ids with `scan_summary` once, later; do not idle or substitute a repo-wide audit.
- `failed`, `stale`, or `SCAN_LIST_EMPTY`: report unavailable evidence, not a clean result. Stop asking for it.

Without usable findings, answer any assigned concrete source questions. If unavailable scans prevent the assigned triage, finish `partial` with that limitation. A completed empty scan can complete a scan-only leg; it does not prove the absence of vulnerabilities or complete a threat-model survey.

## Scanner-triage scope and ceiling

The following source restriction applies to scanner triage, not threat-model survey:

{% include "partials/security-scan-targeted-context.md" %}

Triage at category and rule level. Prioritize high-severity and recurring rules; inspect up to **3 representative findings per requested category** unless evidence conflicts or the assignment requires exhaustive adjudication. Once each requested category has enough evidence to distinguish confirmed positives from scanner noise, finish. Report unreviewed groups and pagination ranges in `remaining_risk`.

Return **confirmed positives (`file:line`) vs. scanner noise** with reasons, not a raw count or a task to triage the scanner later.

{% include "partials/tool-turn-batch.md" %}

{% include "partials/agent-tool-surface.md" %}
{% include "agents/_shell.md" %}
{% include "partials/worker-turn-next-action.md" %}

{% include "partials/finish-handoff.md" %}
