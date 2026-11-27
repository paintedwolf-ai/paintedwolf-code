"""Package memory warning line (80% of ceiling) and bounded review records for ./task budgets."""
import hashlib
import json
from pathlib import Path
import re

from change_report import Finding
from ci_policy import budget_snapshot, resources

ROOT = Path(__file__).resolve().parents[2]
WARNING_PERCENT = 80
MIB = 1024 * 1024
MODULE_PREFIX = "github.com/lycaon/lycaon/"
REVIEWS_DIR = Path(__file__).with_name("memory-reviews")
REQUIRED_FIELDS = frozenset({
    "package", "lane", "workstream", "disposition", "reason",
    "reviewed_commit", "ceiling_bytes", "boundaries",
})


def at_or_above_warning(peak_bytes, limit_bytes):
    """Exact integer comparison against the 80% warning line before any display rounding."""
    return (type(peak_bytes) is int and type(limit_bytes) is int
            and limit_bytes > 0 and peak_bytes * 100 >= limit_bytes * WARNING_PERCENT)


def warning_threshold_bytes(limit_bytes):
    """Largest integer byte count strictly below 80%, so `measured > warn` matches `at_or_above_warning`."""
    return (limit_bytes * WARNING_PERCENT + 99) // 100 - 1


def load_policy():
    """Load resources.json once per report."""
    try:
        return json.loads(resources.POLICY.read_text())
    except (OSError, ValueError):
        return {}


def _ordinary_budget(policy, package):
    value = policy.get("packages", {}).get(package, policy.get("default", {}))
    rss_mib = value.get("rss_mib")
    return rss_mib * MIB if type(rss_mib) is int and rss_mib >= 64 else None


def _lane_for(stage, names, package, limit_bytes, policy):
    if "race" in stage.lower() or any(isinstance(n, str) and "race" in n.lower() for n in names):
        return "race"
    ordinary = _ordinary_budget(policy, package)
    return "race" if ordinary is not None and limit_bytes > ordinary else "ordinary"


def _artifact_id(package):
    if package.startswith(MODULE_PREFIX):
        return "lycaon/" + package[len(MODULE_PREFIX):]
    return package


def collect_receipt_observations(record, label, latest, policy):
    """Merge memory observations from one validated passing receipt into `latest`."""
    stamp = (float(record.get("finished_at") or record.get("started_at") or 0), str(label))
    names = record.get("names") if isinstance(record.get("names"), list) else []
    for evidence in record.get("evidence", []):
        stage = str(evidence.get("stage") or "")
        for package, result in evidence.get("results", {}).items():
            memory = result.get("memory")
            if not isinstance(memory, dict):
                continue
            limit = memory.get("limit_bytes")
            if type(limit) is not int or limit <= 0:
                continue
            lane = _lane_for(stage, names, package, limit, policy)
            obs_key = (lane, package)
            previous = latest.get(obs_key)
            if previous is None or stamp >= previous[0]:
                latest[obs_key] = (stamp, {
                    "lane": lane, "stage": stage, "package": package,
                    "peak_bytes": memory.get("peak_bytes"), "limit_bytes": limit,
                    "source": str(memory.get("source") or "unknown"),
                    "lower_bound": bool(memory.get("lower_bound")),
                })


def _batch_matches_tree(batch_dir, tree_sha):
    """Check the compact batch source.json manifest before reading receipt payloads."""
    manifest = batch_dir / "source.json"
    if not manifest.is_file():
        return True
    try:
        return json.loads(manifest.read_text()).get("source_tree") == tree_sha
    except (OSError, ValueError):
        return True


def latest_observations(artifact_dir, tree_sha):
    """Latest qualifying Linux memory observations for `tree_sha`, keyed by (lane, package).

    Returns (measured_by_key, unavailable_by_lane) where measured values have integer peak_bytes
    and limit_bytes, and unavailable_by_lane maps lane -> sorted list of package names.
    """
    verification_dir = Path(artifact_dir) / "verification"
    if not verification_dir.is_dir():
        return {}, {}
    policy = load_policy()
    latest = {}
    for batch_dir in sorted(p for p in verification_dir.iterdir() if p.is_dir()):
        if not _batch_matches_tree(batch_dir, tree_sha):
            continue
        for receipt in sorted(batch_dir.glob("[0-9]*.json")):
            if receipt.name.endswith(".progress.json"):
                continue
            try:
                record = json.loads(receipt.read_text())
            except (OSError, ValueError):
                continue
            if record.get("status") != "passed" or record.get("source_tree") != tree_sha:
                continue
            collect_receipt_observations(record, receipt, latest, policy)
    measured = {}
    unavailable = {}
    for obs_key, (_, obs) in sorted(latest.items()):
        if type(obs["peak_bytes"]) is int and obs["peak_bytes"] > 0:
            measured[obs_key] = obs
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
    if not isinstance(review["lane"], str) or review["lane"] not in {"ordinary", "race"}:
        raise ValueError("invalid memory review lane")
    if not isinstance(review["workstream"], str) or not re.fullmatch("[a-z0-9-]{1,80}", review["workstream"]):
        raise ValueError("invalid memory review workstream")
    if not isinstance(review["disposition"], str) or review["disposition"] not in {"retain", "refactor"}:
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
    """Load memory review records keyed by (lane, package). Duplicate or malformed records never retain."""
    directory = Path(reviews_dir)
    if not directory.is_dir():
        return {}, []
    records, errors, duplicates = {}, [], set()
    for path in sorted(directory.glob("*.json")):
        try:
            data = json.loads(path.read_text())
        except (OSError, ValueError) as exc:
            errors.append((path, f"malformed JSON: {exc}"))
            continue
        lane = data.get("lane") if isinstance(data, dict) else None
        package = data.get("package") if isinstance(data, dict) else None
        if not isinstance(package, str) or not isinstance(lane, str) or lane not in {"ordinary", "race"}:
            errors.append((path, "missing or invalid package or lane"))
            continue
        review_key = (lane, package)
        if review_key in duplicates or review_key in records:
            records.pop(review_key, None)
            duplicates.add(review_key)
            errors.append((path, f"duplicate review record for {package} ({lane}); neither record is applied"))
            continue
        records[review_key] = (path, data)
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


def tracking_artifacts_from_measured(repo_root, measured, reviews_dir=REVIEWS_DIR):
    """Build maintainability-snapshot tracking rows for packages at or above 80% of ceiling."""
    reviews, _ = load_reviews(reviews_dir)
    rows = []
    for obs_key, obs in sorted(measured.items()):
        if not at_or_above_warning(obs["peak_bytes"], obs["limit_bytes"]):
            continue
        artifact = _artifact_id(obs["package"])
        if not budget_snapshot.relative(artifact):
            continue
        row = {
            "category": f"package_memory_{obs['lane']}",
            "id": artifact,
            "touched": True,
            "measured": obs["peak_bytes"],
            "warn": warning_threshold_bytes(obs["limit_bytes"]),
            "limit": obs["limit_bytes"],
            "effective_cap": obs["limit_bytes"],
            "exception_reason": "",
            "sources": [artifact],
            "spans": [],
        }
        entry = reviews.get(obs_key)
        if entry is not None:
            _, review = entry
            try:
                state = evaluate_review(review, obs, repo_root)
            except ValueError:
                state = None
            if state is not None:
                row["review"] = {
                    "kind": "test_suite",
                    "workstream": review["workstream"],
                    "disposition": review["disposition"],
                    "reason": review["reason"],
                    "reviewed_commit": review["reviewed_commit"],
                    "ceiling": review["ceiling_bytes"],
                    "boundaries": dict(review["boundaries"]),
                    "state": state,
                    "reviewed_shape": "",
                }
        rows.append(row)
    return rows


def tracking_artifacts(repo_root, artifact_dir, tree_sha, reviews_dir=REVIEWS_DIR):
    """Build maintainability-snapshot tracking rows from local qualifying receipts for `tree_sha`."""
    measured, _ = latest_observations(artifact_dir, tree_sha)
    return tracking_artifacts_from_measured(repo_root, measured, reviews_dir=reviews_dir)


def findings(repo_root, artifact_dir, tree_sha, reviews_dir=REVIEWS_DIR):
    """Non-blocking memory warning findings for ./task budgets."""
    measured, unavailable = latest_observations(artifact_dir, tree_sha)
    if not measured and not unavailable:
        return []
    reviews, review_errors = load_reviews(reviews_dir)
    out = []
    for path, detail in review_errors:
        out.append(Finding("warning", "memory", f"invalid memory review record {path.name}: {detail}"))
    for obs_key, obs in sorted(measured.items()):
        if not at_or_above_warning(obs["peak_bytes"], obs["limit_bytes"]):
            continue
        peak_mib = obs["peak_bytes"] / MIB
        limit_mib = obs["limit_bytes"] / MIB
        warn_mib = obs["limit_bytes"] * WARNING_PERCENT / 100 / MIB
        pct = obs["peak_bytes"] * 100 / obs["limit_bytes"]
        source = f"{obs['source']} lower bound" if obs["lower_bound"] else obs["source"]
        prefix = (f"{obs['package']} ({obs['lane']}): {peak_mib:.1f} MiB of {limit_mib:.1f} MiB "
                  f"({pct:.1f}%, {source}), warning {warn_mib:.1f} MiB")
        entry = reviews.get(obs_key)
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
