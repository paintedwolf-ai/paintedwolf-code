"""Comparable execution estimates for a capped hosted matrix."""
import math

from ci_policy.timings import row_key


def valid_seconds(value):
    return type(value) in (int, float) and math.isfinite(value) and value > 0


def order(rows, lanes, timings=None):
    measured = (timings or {}).get("lanes", {})
    complete = all(valid_seconds(measured.get(row_key(row["lane"], row["shard"]))) for row in rows)
    estimates = {}
    for row in rows:
        seconds = measured[row_key(row["lane"], row["shard"])] if complete else row["minutes"] * 60
        estimates[row["lane"]] = max(estimates.get(row["lane"], 0), seconds)
    result = [{**row, "expected_seconds": estimates[row["lane"]],
               "timing_basis": "measured" if complete else "catalog-budget"} for row in rows]
    # Package balancing changes shard assignments; siblings share the lane's longest estimate.
    return sorted(result, key=lambda row: (not lanes[row["lane"]].get("first"),
                                          -row["expected_seconds"], row["lane"], row["shard"]))
