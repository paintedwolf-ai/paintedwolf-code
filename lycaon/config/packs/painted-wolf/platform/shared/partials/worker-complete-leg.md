{% if root_count > 1 %}Finish through **`complete_leg`**, not prose JSON.

- `leg_status`: `complete` | `blocked` | `partial`
- Cite only this leg's observations. Copy tool-result `path` exactly, including its root label; include `line` and verbatim `excerpt`. Excerpts from tool output, such as scanner results, also need that result's handle in `evidence`. Read named-only files before citing them.
{% else %}Finish through **`complete_leg`**, not prose JSON.

- `leg_status`: `complete` | `blocked` | `partial`
- Cite only this leg's observations: a repo-relative `path` you read or grepped, with `line` and verbatim `excerpt`. Excerpts from tool output, such as scanner results, also need that result's handle in `evidence`. Read named-only files before citing them.
{% endif %}
- Status: `complete` = delivery proved; `partial` = work remains; `blocked` = a required project or machine change exceeds your isolated copy. Name that change in `request_decision(blocker_class=sandbox)`.
- `findings[]`: strongest excerpts; counts and report-file requests belong in narrative fields. Optional: `adversary`, `precondition`, and `claim`: `vulnerability` (exploitable within scope), `hardening` (improvement without an in-scope exploit), `accepted_residual` (documented, accepted risk), or `model` (assets, entry points, trust boundaries). Only `vulnerability` retains `severity`: `high` | `medium` | `low`.
- `excerpt` is verbatim file/diff/command text at `path`, never a count or “no result”.
- URLs use `cited_urls`; changed files come from the host. With no repo reads, keep `findings` empty.
{% if profile_has_write_tools and profile_has_verify %}- Source edits need the validation described under **Validate your changes**. Report its scope and limits honestly; incomplete delivery is `partial`, while completed work may have blocked validation. Explicit workflow checks still require passing evidence.
{% endif %}
- Board `Scan:` tails are not proof; cite the `scan#N` result itself.
- `brief`/`objectives_met`: narrative only, ≤**{{ worker_summary_max_chars }}** chars — path facts in `findings[]`; prose path names are advisory, not proof.
- If `complete_leg` is rejected, branch on the `Code:` and resend the whole report. A `WORKER_*` citation code lists what to fix or drop; keep everything else, and set `leg_status` from the evidence that remains. `WORKER_SUMMARY_TOO_LONG`: shorten the narrative fields, never `findings[]`.
- The final tool round offers only `complete_leg`; use it.
