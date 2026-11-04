"""Comparable execution estimates for a capped hosted matrix."""
import heapq

from ci_policy.timings import valid_seconds

PRIORITIES = {"reject": 0, "critical": 1, "compile": 2, "normal": 3}


def order(rows, lanes, timings=None, capacity=4):
    measured = (timings or {}).get("lanes", {})
    missing = [row["timing_row"] for row in rows
               if not valid_seconds(measured.get(row["timing_row"]))]
    complete = not missing
    estimates = {}
    for row in rows:
        seconds = measured[row["timing_row"]] if complete else row["minutes"] * 60
        estimates[row["lane"]] = max(estimates.get(row["lane"], 0), seconds)
    result = [{**row, "expected_seconds": estimates[row["lane"]],
               "timing_basis": "measured" if complete else "catalog-budget",
               "priority": lanes[row["lane"]].get("priority", "normal")} for row in rows]
    # Package balancing changes shard assignments; siblings share the lane's longest estimate.
    baseline = sorted(result, key=lambda row: (min(PRIORITIES[row['priority']], 2),
                                               -row['expected_seconds'], row['lane'], row['shard']))
    return promote_compilation(baseline, capacity)


def duration(rows, capacity):
    slots = [0] * capacity
    for row in rows:
        finish = heapq.heappop(slots) + row['expected_seconds']
        heapq.heappush(slots, finish)
    return max(slots)


def promote_compilation(rows, capacity):
    limit = duration(rows, capacity)
    boundary = sum(row['priority'] in {'reject', 'critical'} for row in rows)
    for row in [row for row in rows if row['priority'] == 'compile']:
        index = rows.index(row)
        for position in range(boundary, index):
            candidate = [*rows[:position], row, *rows[position:index], *rows[index + 1:]]
            if duration(candidate, capacity) <= limit:
                rows = candidate
                break
    return rows


def summarize(matrix, timings=None):
    rows = matrix["include"]
    measured = (timings or {}).get("lanes", {})
    missing = [row["timing_row"] for row in rows
               if not valid_seconds(measured.get(row["timing_row"]))]
    lines = ["### Verification schedule", "", f"Estimate basis: `{rows[0]['timing_basis']}`.", ""]
    if missing:
        lines += ["Missing measurements: " + ", ".join(f"`{key}`" for key in missing) + ".", ""]
    runs = (timings or {}).get("runs", [])
    if runs:
        lines += ["Nightly timing sources: " + ", ".join(str(run) for run in runs) + ".", ""]
    lines += ["| Order | Lane | Shard | Priority | Execution estimate (seconds) | Timing row | Measurement run |",
              "|---|---|---|---|---|---|---|"]
    for index, row in enumerate(rows, 1):
        source = (timings or {}).get("lane_runs", {}).get(row["timing_row"], "")
        if row["timing_basis"] != "measured":
            source = ""
        lines.append(f"| {index} | {row['lane']} | {row['shard']} | {row['priority']} | {row['expected_seconds']} | {row['timing_row']} | {source} |")
    return "\n".join(lines) + "\n"
