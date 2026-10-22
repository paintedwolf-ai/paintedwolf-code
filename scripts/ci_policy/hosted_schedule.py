"""Comparable execution estimates for a capped hosted matrix."""
from ci_policy.timings import row_key, valid_seconds


def order(rows, lanes, timings=None):
    measured = (timings or {}).get("lanes", {})
    missing = [row_key(row["lane"], row["shard"]) for row in rows
               if not valid_seconds(measured.get(row_key(row["lane"], row["shard"])))]
    complete = not missing
    estimates = {}
    for row in rows:
        seconds = measured[row_key(row["lane"], row["shard"])] if complete else row["minutes"] * 60
        estimates[row["lane"]] = max(estimates.get(row["lane"], 0), seconds)
    result = [{**row, "expected_seconds": estimates[row["lane"]],
               "timing_basis": "measured" if complete else "catalog-budget"} for row in rows]
    # Package balancing changes shard assignments; siblings share the lane's longest estimate.
    return sorted(result, key=lambda row: (not lanes[row["lane"]].get("first"),
                                          -row["expected_seconds"], row["lane"], row["shard"]))


def summarize(matrix, timings=None):
    rows = matrix["include"]
    measured = (timings or {}).get("lanes", {})
    missing = [row_key(row["lane"], row["shard"]) for row in rows
               if not valid_seconds(measured.get(row_key(row["lane"], row["shard"])))]
    lines = ["### Verification schedule", "", f"Estimate basis: `{rows[0]['timing_basis']}`.", ""]
    if missing:
        lines += ["Missing measurements: " + ", ".join(f"`{key}`" for key in missing) + ".", ""]
    runs = (timings or {}).get("runs", [])
    if runs:
        lines += ["Qualification timing sources: " + ", ".join(str(run) for run in runs) + ".", ""]
    lines += ["| Order | Lane | Shard | Execution estimate (seconds) | Measurement run |",
              "|---|---|---|---|---|"]
    for index, row in enumerate(rows, 1):
        source = (timings or {}).get("lane_runs", {}).get(row_key(row["lane"], row["shard"]), "")
        if row["timing_basis"] != "measured":
            source = ""
        lines.append(f"| {index} | {row['lane']} | {row['shard']} | {row['expected_seconds']} | {source} |")
    return "\n".join(lines) + "\n"
