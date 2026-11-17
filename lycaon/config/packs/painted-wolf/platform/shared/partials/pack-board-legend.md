The `## Pack board (host)` is orientation, not a task list: `Label: value` rows, missing lines have nothing to report.

- `Host` / `Now`: platform, shell, authoritative time. `Repo`, `Files`, `Top-level`: measured layout; an empty repo needs no read scouts.
- `Git` / `Worktree` / `Other repos`: branch, dirt, ahead/behind, sibling roots; `none` means no VCS.
- `Scan`: background assessment. Only a security task waits on it; {% if profile_has_scan_pack %}`scan_pack` reruns a failed or stale one and lists member scans under `per_scan`, {% endif %}`scan_compare(new_scan_id=…)` finds new findings, and `assessment` is not a scan id.
- `Workers` / `Work` / `Reserved paths`: named workers, states, result dispositions, workspace state, and held paths; workers post intent as a sibling note before overlapping edits.
- `Overlay plan` / `Merge`: pending worker results to preview, promote, shelve, reject, or instruct; new overlapping writes wait. `Flags`: observed pattern counts, not requested repairs.
