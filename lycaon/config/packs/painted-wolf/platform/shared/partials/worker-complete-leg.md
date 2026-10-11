Finish through **`complete_leg`**, not prose JSON.

- `leg_status`: `complete` | `blocked` | `partial`
- Source citations require an observed path, line, and verbatim excerpt. {% if root_count > 1 %}Preserve the tool result's root label.{% else %}Use repo-relative paths.{% endif %} For source claims, `evidence` must be the matching source observation (for example `read#N`), never the scanner that led you there. Scanner-only findings use `evidence: "scan#N"` with no `path`, `line`, or `excerpt`; explain the result in `note`. Read the source before making a source claim.
- Record unexamined or inconclusive scope in `coverage_gaps`, even without findings. Use `[]` when none remain.
- Status: `complete` = delivery proved; `partial` = work remains; `blocked` = a required project or machine change exceeds your isolated copy. Name that change in `request_decision(blocker_class=sandbox)`.
- `findings[]`: strongest excerpts; counts and report-file requests belong in narrative fields. Optional: `adversary`, `precondition`, and `claim`: `vulnerability` (exploitable within scope), `hardening` (improvement without an in-scope exploit), `accepted_residual` (documented, accepted risk), or `model` (assets, entry points, trust boundaries). Only `vulnerability` retains `severity`: `high` | `medium` | `low`.
- `excerpt` is verbatim file/diff/command text at `path`, never a count or “no result”.
- URLs use `cited_urls`, never `findings[].path`. For web findings, use the observed `web#N` in `evidence` and explain the conclusion in `note`; omit source coordinates. Changed files come from the host.
- Evidence is scoped to this job. After a child resume, reacquire the source or URL before citing an earlier job’s observation; transcript continuity does not carry its evidence into this job.
{% if profile_has_write_tools and profile_has_verify %}- Source edits need the validation described under **Validate your changes**. Report its scope and limits honestly; incomplete delivery is `partial`, while completed work may have blocked validation. Explicit workflow checks still require passing evidence.
{% endif %}
- Board `Scan:` tails are not proof; cite the `scan#N` result itself.
- `brief`/`objectives_met`: narrative only, ≤**{{ worker_summary_max_chars }}** chars — path facts in `findings[]`; prose path names are advisory, not proof.
- If `complete_leg` is rejected, branch on the `Code:` and resend the whole report. A `WORKER_*` citation code lists what to fix or drop; keep everything else, and set `leg_status` from the evidence that remains. `WORKER_SUMMARY_TOO_LONG`: shorten the narrative fields, never `findings[]`.
- The final tool round offers only `complete_leg`; use it.
