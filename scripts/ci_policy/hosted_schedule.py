"""Comparable execution estimates for a capped hosted matrix."""
from ci_policy.timings import valid_seconds

PRIORITIES = {"reject": 0, "critical": 1, "normal": 2}


def unmeasured(row, lanes, measured):
    """A row nightly should have measured and did not. Nightly cannot run a lane scoped to a change,
    so such a lane keeps its budget without moving the rest of the matrix off its measurements."""
    return "nightly" in lanes[row["lane"]]["profiles"] and not valid_seconds(measured.get(row["timing_row"]))


def order(rows, lanes, timings=None):
    measured = (timings or {}).get("lanes", {})
    complete = not any(unmeasured(row, lanes, measured) for row in rows)
    estimates = {}
    for row in rows:
        seconds = measured[row["timing_row"]] if complete and valid_seconds(measured.get(row["timing_row"])) \
            else row["minutes"] * 60
        estimates[row["lane"]] = max(estimates.get(row["lane"], 0), seconds)
    result = [{**row, "expected_seconds": estimates[row["lane"]],
               "timing_basis": "measured" if complete else "catalog-budget",
               "priority": lanes[row["lane"]].get("priority", "normal")} for row in rows]
    # Package balancing changes shard assignments; siblings share the lane's longest estimate.
    return sorted(result, key=lambda row: (PRIORITIES[row['priority']], -row['expected_seconds'], row['lane'], row['shard']))


def summarize(matrix, timings=None):
    rows = matrix["include"]
    if not rows:
        return "### Verification schedule\n\nNo lanes: the static job verifies this change.\n"
    measured = (timings or {}).get("lanes", {})
    missing = [row["timing_row"] for row in rows
               if not valid_seconds(measured.get(row["timing_row"]))]
    lines = ["### Verification schedule", "", f"Estimate basis: `{rows[0]['timing_basis']}`.", ""]
    if missing:
        lines += ["Missing measurements: " + ", ".join(f"`{key}`" for key in missing) + ".", ""]
    run = (timings or {}).get("run")
    if run:
        lines += [f"Nightly timing source: run {run}.", ""]
    lines += ["| Order | Lane | Shard | Priority | Execution estimate (seconds) | Timing row |",
              "|---|---|---|---|---|---|"]
    for index, row in enumerate(rows, 1):
        lines.append(f"| {index} | {row['lane']} | {row['shard']} | {row['priority']} | {row['expected_seconds']} | {row['timing_row']} |")
    return "\n".join(lines) + "\n"
