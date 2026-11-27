"""Package memory warning line (80% of ceiling) and bounded review records for ./task budgets."""
import hashlib
import json
from pathlib import Path
import re

from change_report import Finding
from ci_policy import budget_snapshot, resources

WARNING_PERCENT = 80
MIB = 1024 * 1024
REVIEWS_DIR = Path(__file__).with_name("memory-reviews")
REQUIRED_FIELDS = frozenset({
    "package", "lane", "workstream", "disposition", "reason",
    "reviewed_commit", "ceiling_bytes", "boundaries",
})


def at_or_above_warning(peak_bytes, limit_bytes):
    """Exact integer comparison against the 80% warning line before any display rounding."""
    return type(peak_bytes) is int and type(limit_bytes) is int and limit_bytes > 0 and peak_bytes * 100 >= limit_bytes * WARNING_PERCENT


def _lane_for(stage, package, limit_bytes):
    if "race" in stage.lower():
        return "race"
    try:
        ordinary = resources.budget(package, race=False)
    except (OSError, ValueError):
        return "ordinary"
    return "race" if limit_bytes > ordinary else "ordinary"


def latest_observations(artifact_dir, tree_sha):
    """Latest qualifying Linux memory observations for `tree_sha`, keyed by (lane, package).

    Returns (measured_by_key, unavailable_by_lane) where measured values have integer peak_bytes
    and limit_bytes, and unavailable_by_lane maps lane -> sorted list of package names.
    """
    verification_dir = Path(artifact_dir) / "verification"
    if not verification_dir.is_dir():
        return {}, {}
    latest = {}
    for receipt in sorted(verification_dir.glob("*/[0-9]*.json")):
        if receipt.name.endswith(".progress.json"):
            continue
        try:
            record = json.loads(receipt.read_text())
        except (OSError, ValueError):
            continue
        if record.get("status") != "passed" or record.get("source_tree") != tree_sha:
            continue
        stamp = (float(record.get("finished_at") or record.get("started_at") or 0), str(receipt))
        for evidence in record.get("evidence", []):
            stage = str(evidence.get("stage") or "")
            for package, result in evidence.get("results", {}).items():
                memory = result.get("memory")
                if not isinstance(memory, dict):
                    continue
                limit = memory.get("limit_bytes")
                if type(limit) is not int or limit <= 0:
                    continue
                lane = _lane_for(stage, package, limit)
                key = (lane, package)
                previous = latest.get(key)
                if previous is None or stamp >= previous[0]:
                    latest[key] = (stamp, {
                        "lane": lane, "stage": stage, "package": package,
                        "peak_bytes": memory.get("peak_bytes"), "limit_bytes": limit,
                        "source": str(memory.get("source") or "unknown"),
                        "lower_bound": bool(memory.get("lower_bound")),
                    })
    measured = {}
    unavailable = {}
    for key, (_, obs) in sorted(latest.items()):
        if type(obs["peak_bytes"]) is int and obs["peak_bytes"] > 0:
            measured[key] = obs
        else:
            unavailable.setdefault(obs["lane"], []).append(obs["package"])
    return measured, unavailable


def _file_sha256(path):
    if path.is_symlink() or not path.is_file():
        return None
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _validate_review(review, limit_bytes):
    if not isinstance(review, dict) or set(review) != REQUIRED_FIELDS:
        raise ValueError("invalid memory review shape")
    if not isinstance(review["package"], str) or not review["package"].strip():
        raise ValueError("invalid memory review package")
    if review["lane"] not in {"ordinary", "race"}:
        raise ValueError("invalid memory review lane")
    if not isinstance(review["workstream"], str) or not re.fullmatch("[a-z0-9-]{1,80}", review["workstream"]):
        raise ValueError("invalid memory review workstream")
    if review["disposition"] not in {"retain", "refactor"}:
        raise ValueError("invalid memory review disposition")
    if (not isinstance(review["reason"], str) or not review["reason"].strip() or len(review["reason"]) > 4096
            or not isinstance(review["reviewed_commit"], str) or not re.fullmatch("[0-9a-f]{40}", review["reviewed_commit"])):
        raise ValueError("missing memory review provenance")
    if type(review["ceiling_bytes"]) is not int or not isinstance(review["boundaries"], dict):
        raise ValueError("invalid memory review bounds")
    if review["disposition"] == "refactor":
        if review["ceiling_bytes"] != 0 or review["boundaries"]:
            raise ValueError("refactor review cannot set ceiling_bytes or boundaries")
        return
    if review["ceiling_bytes"] * 100 <= limit_bytes * WARNING_PERCENT or review["ceiling_bytes"] > limit_bytes:
        raise ValueError("retained memory ceiling must be above 80% warning line and at or below hard ceiling")
    if not 1 <= len(review["boundaries"]) <= 32:
        raise ValueError("retained memory review must pin 1 to 32 boundary contracts")
    for rel_path, digest in review["boundaries"].items():
        if not budget_snapshot.relative(rel_path) or not isinstance(digest, str) or not re.fullmatch("[0-9a-f]{64}", digest):
            raise ValueError("invalid memory review boundary")


def load_reviews(reviews_dir):
    """Load memory review records keyed by (lane, package). Malformed records are returned as errors."""
    directory = Path(reviews_dir)
    if not directory.is_dir():
        return {}, []
    records, errors = {}, []
    for path in sorted(directory.glob("*.json")):
        try:
            data = json.loads(path.read_text())
        except (OSError, ValueError) as exc:
            errors.append((path, f"malformed JSON: {exc}"))
            continue
        if not isinstance(data, dict) or not isinstance(data.get("package"), str) or data.get("lane") not in {"ordinary", "race"}:
            errors.append((path, "missing package or lane"))
            continue
        key = (data["lane"], data["package"])
        if key in records:
            errors.append((path, f"duplicate review record for {data['package']} ({data['lane']})"))
            continue
        records[key] = (path, data)
    return records, errors


def evaluate_review(review, obs, repo_root):
    """Evaluate a review record against current measurement and repo boundary files."""
    _validate_review(review, obs["limit_bytes"])
    if review["disposition"] == "refactor":
        return "refactor"
    if obs["peak_bytes"] > review["ceiling_bytes"]:
        return "ceiling_exceeded"
    for rel_path, expected in review["boundaries"].items():
        if _file_sha256(Path(repo_root) / rel_path) != expected:
            return "boundary_changed"
    return "retained"


def findings(repo_root, artifact_dir, tree_sha, reviews_dir=REVIEWS_DIR):
    """Non-blocking memory warning findings for ./task budgets."""
    measured, unavailable = latest_observations(artifact_dir, tree_sha)
    if not measured and not unavailable:
        return []
    reviews, review_errors = load_reviews(reviews_dir)
    out = []
    for path, detail in review_errors:
        out.append(Finding("warning", "memory", f"invalid memory review record {path.name}: {detail}"))
    for key, obs in sorted(measured.items()):
        if not at_or_above_warning(obs["peak_bytes"], obs["limit_bytes"]):
            continue
        peak_mib = obs["peak_bytes"] / MIB
        limit_mib = obs["limit_bytes"] / MIB
        warn_mib = obs["limit_bytes"] * WARNING_PERCENT / 100 / MIB
        pct = obs["peak_bytes"] * 100 / obs["limit_bytes"]
        source = f"{obs['source']} lower bound" if obs["lower_bound"] else obs["source"]
        prefix = (f"{obs['package']} ({obs['lane']}): {peak_mib:.1f} MiB of {limit_mib:.1f} MiB "
                  f"({pct:.1f}%, {source}), warning {warn_mib:.1f} MiB")
        entry = reviews.get(key)
        if entry is None:
            out.append(Finding("warning", "memory", f"{prefix}; review retention and ownership or record a bounded review"))
            continue
        path, review = entry
        try:
            state = evaluate_review(review, obs, repo_root)
        except ValueError as exc:
            out.append(Finding("warning", "memory", f"{prefix}; invalid review record {path.name}: {exc}"))
            continue
        if state == "retained":
            out.append(Finding("notice", "memory",
                               f"{prefix}; design decision retain (retained up to {review['ceiling_bytes'] / MIB:.1f} MiB); "
                               f"workstream {review['workstream']}; {review['reason']}"))
        else:
            out.append(Finding("warning", "memory",
                               f"{prefix}; review record {path.name} requires renewal ({state}); "
                               f"workstream {review['workstream']}; {review['reason']}"))
    for lane, packages in sorted(unavailable.items()):
        out.append(Finding("info", "memory",
                           f"{len(packages)} {lane} package(s) had unavailable memory observations "
                           f"(not checked against the {WARNING_PERCENT}% warning line): {', '.join(packages[:5])}"
                           + (f" (+{len(packages) - 5} more)" if len(packages) > 5 else "")))
    return out
