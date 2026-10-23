"""Admit compiler cache writes within a conservative repository storage budget."""
import glob
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

from ci_policy.github import api, pages
from ci_policy.cache_payload import compressed_size

LIMIT = 8 * 1024**3
GO_BUILD_LIMIT = 1024**3
FAMILY = re.compile(r"^(go(?:-v[0-9]+)?|decide-v[0-9]+|den-rust-v[0-9]+|release-build|document-core-v[0-9]+)-(Linux|macOS)-(X64|ARM64)-")


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
    # Reserve the measured compressed payload with archive variance headroom.
    reserve = size + size // 20 + 1024**2
    total = sum(row["size_in_bytes"] for row in rows)
    owned = [row for row in rows if row["ref"] == "refs/heads/main" and family(row["key"]) is not None]
    replaced = [row for row in owned if family(row["key"]) == owner]
    others = sorted([row for row in owned if family(row["key"]) != owner],
                    key=lambda row: (row.get("last_accessed_at", ""), -row["size_in_bytes"]))
    remove = []
    for row in replaced + others:
        if total + reserve <= limit:
            break
        total -= row["size_in_bytes"]
        remove.append(row["id"])
    allowed = total + reserve <= limit
    return {"allowed": allowed, "reason": "within budget" if allowed else "payload or unowned caches exceed budget",
            "delete": remove if allowed else [],
            "retire_after_save": [row["id"] for row in replaced if row["id"] not in remove] if allowed else [],
            "stored_bytes": sum(row["size_in_bytes"] for row in rows),
            "reserved_bytes": reserve, "projected_bytes": total + reserve, "limit_bytes": limit}


def confirm(rows, record):
    key = record["key"]
    saved = [row for row in rows if row["key"] == key and row["ref"] == "refs/heads/main" and row["size_in_bytes"] > 0]
    if len(saved) != 1:
        return {"published": False, "delete": [], "reason": "new cache entry not confirmed; retained prior generations"}
    pending = set(record["retire_after_save"])
    retired = [row["id"] for row in rows if row["id"] in pending and row["ref"] == "refs/heads/main"
               and row["key"] != key and family(row["key"]) == family(key)]
    return {"published": True, "delete": retired, "cache_id": saved[0]["id"],
            "measured_compressed_bytes": record["measured_compressed_bytes"],
            "stored_compressed_bytes": saved[0]["size_in_bytes"], "reserved_bytes": record["reserved_bytes"]}


def main():
    if (os.environ.get("GITHUB_REF") != "refs/heads/main" or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise ValueError("compiler publication requires the trusted main hosted warmer")
    key = os.environ["CACHE_KEY"]
    repository = os.environ["GITHUB_REPOSITORY"]
    inventory = f"repos/{repository}/actions/caches"
    if os.environ.get("CACHE_RECORD"):
        record = json.loads(Path(os.environ["CACHE_RECORD"]).read_text())
        if key != record["key"] or not record["allowed"]:
            raise ValueError("cache confirmation does not match its admitted publication")
        result = confirm(list(pages(inventory, "actions_caches")), record)
    else:
        if family(key) and family(key)[0] == "go":
            path = Path(subprocess.check_output(["go", "env", "GOCACHE"], text=True).strip())
            trim_go(path)
        payload = files(os.environ["CACHE_PATHS"])
        size = compressed_size(payload)
        result = {**plan(list(pages(inventory, "actions_caches")), key, size), "key": key,
                  "uncompressed_bytes": sum(path.stat().st_size for path in payload), "measured_compressed_bytes": size}
        with tempfile.NamedTemporaryFile(mode="w", prefix="cache-admission-", suffix=".json", delete=False,
                                         dir=os.environ["RUNNER_TEMP"]) as record:
            json.dump(result, record)
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"allowed={str(result['allowed']).lower()}\nrecord={record.name}\n")
    for cache_id in result["delete"]:
        api(f"{inventory}/{cache_id}", "DELETE")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write("### Compiler cache publication\n\n```json\n" + json.dumps({"key": key, **result}, indent=2) + "\n```\n")
    if not result.get("allowed", result.get("published")):
        print(f"::notice::Compiler cache not published: {result['reason']}")


if __name__ == "__main__":
    main()
