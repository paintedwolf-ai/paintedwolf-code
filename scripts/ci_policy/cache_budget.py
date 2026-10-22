"""Admit compiler cache writes within a conservative repository storage budget."""
import glob
import json
import os
from pathlib import Path
import re
import subprocess

from ci_policy.github import api, pages

LIMIT = 8 * 1024**3
GO_BUILD_LIMIT = 1024**3
FAMILY = re.compile(r"^(go(?:-v[0-9]+)?|decide-v[0-9]+|den-rust-v[0-9]+|release-build)-(Linux|macOS)-(X64|ARM64)-")


def family(key):
    match = FAMILY.match(key)
    if not match:
        return None
    return (re.sub(r"-v[0-9]+$", "", match[1]), match[2], match[3])


def files(patterns):
    included, excluded = set(), set()
    for pattern in patterns.splitlines():
        if not pattern.strip():
            continue
        target = excluded if pattern.startswith("!") else included
        for value in glob.glob(os.path.expanduser(pattern.lstrip("!")), recursive=True):
            path = Path(value)
            if path.is_symlink():
                continue
            if path.is_file():
                target.add(path.resolve())
            elif path.is_dir():
                target.update(p.resolve() for p in path.rglob("*") if p.is_file() and not p.is_symlink())
    return included - excluded


def trim_go(path, limit=GO_BUILD_LIMIT):
    entries = sorted(files(str(path)), key=lambda p: (p.stat().st_mtime_ns, str(p)))
    total = sum(p.stat().st_size for p in entries)
    for entry in entries:
        if total <= limit:
            break
        if not re.fullmatch(r"[0-9a-f]{64}-[ad]", entry.name):
            continue
        total -= entry.stat().st_size
        entry.unlink()
    return total


def plan(rows, key, size, limit=LIMIT):
    owner = family(key)
    if owner is None or size < 0:
        raise ValueError("cache publication requires a declared compiler family and nonnegative size")
    if size == 0:
        return {"allowed": False, "reason": "no cache payload", "delete": []}
    if any(row["key"] == key and row["ref"] == "refs/heads/main" for row in rows):
        return {"allowed": False, "reason": "exact key already exists", "delete": []}
    # Reserve uncompressed bytes and archive headroom before the upload.
    reserve = size + size // 20 + 1024**2
    total = sum(row["size_in_bytes"] for row in rows)
    owned = [row for row in rows if row["ref"] == "refs/heads/main" and family(row["key"]) is not None]
    replaced = [row for row in owned if family(row["key"]) == owner]
    others = sorted([row for row in owned if family(row["key"]) != owner],
                    key=lambda row: (row.get("last_accessed_at", ""), -row["size_in_bytes"]))
    remove = []
    for row in replaced + others:
        if row not in replaced and total + reserve <= limit:
            break
        total -= row["size_in_bytes"]
        remove.append(row["id"])
    allowed = total + reserve <= limit
    return {"allowed": allowed, "reason": "within budget" if allowed else "payload or unowned caches exceed budget",
            "delete": remove if allowed else [], "stored_bytes": sum(row["size_in_bytes"] for row in rows),
            "reserved_bytes": reserve, "projected_bytes": total + reserve, "limit_bytes": limit}


def main():
    if (os.environ.get("GITHUB_REF") != "refs/heads/main" or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise ValueError("compiler publication requires the trusted main hosted warmer")
    key = os.environ["CACHE_KEY"]
    if family(key) and family(key)[0] == "go":
        path = Path(subprocess.check_output(["go", "env", "GOCACHE"], text=True).strip())
        trim_go(path)
    size = sum(path.stat().st_size for path in files(os.environ["CACHE_PATHS"]))
    repository = os.environ["GITHUB_REPOSITORY"]
    rows = list(pages(f"repos/{repository}/actions/caches", "actions_caches"))
    result = plan(rows, key, size)
    for cache_id in result["delete"]:
        api(f"repos/{repository}/actions/caches/{cache_id}", "DELETE")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"allowed={str(result['allowed']).lower()}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write("### Compiler cache admission\n\n```json\n" + json.dumps({"key": key, **result}, indent=2) + "\n```\n")
    if not result["allowed"]:
        print(f"::notice::Compiler cache not published: {result['reason']}")


if __name__ == "__main__":
    main()
