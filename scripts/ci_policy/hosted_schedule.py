"""Comparable execution estimates for a capped hosted matrix."""
from ci_policy.timings import valid_seconds

PRIORITIES = {"reject": 0, "critical": 1, "normal": 2}


def order(rows, lanes, timings=None):
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
    return sorted(result, key=lambda row: (PRIORITIES[row['priority']], -row['expected_seconds'], row['lane'], row['shard']))


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
