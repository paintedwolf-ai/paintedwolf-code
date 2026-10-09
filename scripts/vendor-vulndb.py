#!/usr/bin/env python3
"""Materialize the Go vulnerability database `lint:vuln` reads.

The database is pinned so one source gives one verdict and a passing stage can
be replayed; it contains the complete upstream snapshot, so dependency changes do not
refresh advisories or omit modules newly entering the build graph. Freshness against upstream is `lint:vuln:fresh`.

`go list -m all` never names the Go distribution, which govulncheck looks up
under the pseudo-modules `stdlib` and `toolchain`; without their index rows no
standard-library advisory is ever read.
"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import json
import shutil
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GO_DIR = ROOT / "lycaon"
VENDOR = GO_DIR / "vulndb"
PROVENANCE = GO_DIR / "vulndb-provenance.json"
UPSTREAM = "https://vuln.go.dev"
FETCH_TIMEOUT = 60
FETCH_ATTEMPTS = 4
GO_PSEUDO_MODULES = ("stdlib", "toolchain")


def transient(error: Exception) -> bool:
    if isinstance(error, urllib.error.HTTPError):
        return error.code == 429 or error.code >= 500
    return isinstance(error, (urllib.error.URLError, TimeoutError))


def fetch(endpoint: str) -> bytes:
    url = f"{UPSTREAM}/{endpoint}.json.gz"
    for attempt in range(1, FETCH_ATTEMPTS + 1):
        try:
            with urllib.request.urlopen(url, timeout=FETCH_TIMEOUT) as response:  # noqa: S310 — pinned https host
                return gzip.decompress(response.read())
        except Exception as error:  # noqa: BLE001 — classified by transient()
            if attempt == FETCH_ATTEMPTS or not transient(error):
                raise SystemExit(f"vendor-vulndb: {url}: {error}") from error
            time.sleep(2**attempt)
    raise AssertionError("unreachable")


def tree_digest(directory: Path) -> str:
    """One digest over the vendored bytes, stable across filesystems."""
    digest = hashlib.sha256()
    for path in sorted(p for p in directory.rglob("*") if p.is_file()):
        digest.update(str(path.relative_to(directory)).encode())
        digest.update(b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=1, sort_keys=True) + "\n", encoding="utf-8")


def refresh() -> int:
    kept = json.loads(fetch("index/modules"))
    absent = missing_pseudo_modules(kept)
    if absent:
        raise SystemExit(f"vendor-vulndb: upstream index has no rows for {', '.join(absent)}")
    ids = sorted({vuln["id"] for entry in kept for vuln in entry.get("vulns", [])})
    db = json.loads(fetch("index/db"))
    vulns = json.loads(fetch("index/vulns"))
    # Fetch everything before replacing the vendored tree so a failed refresh
    # leaves the previous pin intact.
    from concurrent.futures import ThreadPoolExecutor
    with ThreadPoolExecutor(max_workers=8) as pool:
        entries = dict(zip(ids, pool.map(lambda ident: json.loads(fetch(f"ID/{ident}")), ids)))

    if VENDOR.exists():
        shutil.rmtree(VENDOR)
    write_json(VENDOR / "index" / "modules.json", kept)
    write_json(VENDOR / "index" / "db.json", db)
    write_json(VENDOR / "index" / "vulns.json", [v for v in vulns if v.get("id") in ids])
    for vuln_id, entry in entries.items():
        write_json(VENDOR / "ID" / f"{vuln_id}.json", entry)

    write_json(
        PROVENANCE,
        {
            "source": UPSTREAM,
            "database_modified": db.get("modified", ""),
            "coverage": "complete",
            "covered_modules": len(kept),
            "entries": len(ids),
            "tree_sha256": tree_digest(VENDOR),
        },
    )
    print(
        f"vendor-vulndb: {len(ids)} entries covering all {len(kept)} indexed modules "
        f"(database modified {db.get('modified', 'unknown')})",
        file=sys.stderr,
    )
    return 0


def missing_pseudo_modules(index: list[dict]) -> list[str]:
    present = {entry.get("path") for entry in index}
    return [path for path in GO_PSEUDO_MODULES if path not in present]


def check() -> int:
    """Offline: the vendored bytes match their pin, and the pin matches this module."""
    if not PROVENANCE.is_file() or not VENDOR.is_dir():
        print("vendor-vulndb: no vendored database; run ./task lint:vuln:vendor", file=sys.stderr)
        return 1
    pin = json.loads(PROVENANCE.read_text(encoding="utf-8"))

    actual = tree_digest(VENDOR)
    if actual != pin.get("tree_sha256"):
        print(
            "vendor-vulndb: vendored bytes do not match their pin; "
            "re-run ./task lint:vuln:vendor rather than editing them",
            file=sys.stderr,
        )
        return 1

    if pin.get("coverage") != "complete":
        print("vendor-vulndb: a complete snapshot is required; run ./task lint:vuln:vendor", file=sys.stderr)
        return 1

    index = VENDOR / "index" / "modules.json"
    covered = json.loads(index.read_text(encoding="utf-8"))
    absent = missing_pseudo_modules(covered)
    if absent:
        print(
            f"vendor-vulndb: index has no rows for {', '.join(absent)}, so Go distribution "
            "advisories go unread; run ./task lint:vuln:vendor",
            file=sys.stderr,
        )
        return 1
    ids = {vuln["id"] for entry in covered for vuln in entry.get("vulns", [])}
    missing = sorted(i for i in ids if not (VENDOR / "ID" / f"{i}.json").is_file())
    if missing:
        print(f"vendor-vulndb: index references absent entries: {', '.join(missing)}", file=sys.stderr)
        return 1
    print(f"vendor-vulndb: {len(ids)} entries pinned at {pin.get('database_modified', 'unknown')}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="verify the pin without network access")
    args = parser.parse_args()
    return check() if args.check else refresh()


if __name__ == "__main__":
    sys.exit(main())
