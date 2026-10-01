The `## Pack board (host)` is orientation, not a task list: `Label: value` rows, missing lines have nothing to report.

- `Host` / `Now`: platform, shell, authoritative time. `Repo`, `Files`, `Top-level`: measured layout; an empty repo needs no read scouts.
- `Git` / `Worktree` / `Other repos`: branch, dirt, ahead/behind, sibling roots; `none` means no VCS.
- `Scan`: background assessment. Only a security task waits on it; `scan_pack` reruns a failed or stale one and lists member scans under `per_scan`, `scan_compare(new_scan_id=…)` finds new findings, and `assessment` is not a scan id.
- `Work` / `Task` / `In flight` / `Reserved paths`: jobs, roles, budgets; post intent with `record_finding` before overlapping edits.
- `Overlay plan` / `Merge` / `Merge paths`: pending worker writes to land or reject before new write dispatch. `Flags`: observed pattern counts, not requested repairs.
