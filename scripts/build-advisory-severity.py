#!/usr/bin/env python3
"""Build the bundled advisory severity catalog from NVD CVSS records.

The engine embeds the output (lycaon/config/runtime/scanners/advisory-severity.json)
and derives each base score from its vector, so rows carry no score.
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUTPUT_FILE = ROOT / "lycaon" / "config" / "runtime" / "scanners" / "advisory-severity.json"

# NVD metric families in preference order, with the catalog type each carries.
NVD_METRICS = (
    ("cvssMetricV31", "CVSS_V3"),
    ("cvssMetricV30", "CVSS_V3"),
    ("cvssMetricV40", "CVSS_V4"),
)


def normalize_id(advisory_id: str) -> str:
    return advisory_id.strip().upper()


def primary_metric(entries: list[dict]) -> dict | None:
    """NVD's own assessment is the Primary source; CNA scores are Secondary."""
    for entry in entries:
        if entry.get("type") == "Primary":
            return entry
    return entries[0] if entries else None


def fetch_cve_cvss(cve_id: str) -> dict | None:
    """Fetch the preferred CVSS vector for one CVE from NVD."""
    url = f"https://services.nvd.nist.gov/rest/json/cves/2.0?cveId={cve_id}"
    req = urllib.request.Request(url, headers={"User-Agent": "PaintedWolf-SeveritySync/1.0"})
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            if resp.status != 200:
                return None
            data = json.loads(resp.read().decode("utf-8"))
    except Exception as err:
        print(f"Failed to fetch {cve_id}: {err}", file=sys.stderr)
        return None
    vulns = data.get("vulnerabilities", [])
    if not vulns:
        return None
    metrics = vulns[0].get("cve", {}).get("metrics", {})
    for key, cvss_type in NVD_METRICS:
        metric = primary_metric(metrics.get(key, []))
        vector = (metric or {}).get("cvssData", {}).get("vectorString")
        if vector:
            return {"id": cve_id, "type": cvss_type, "vector": vector, "source": "nvd.cvss"}
    return None


def main() -> None:
    parser = argparse.ArgumentParser(description="Update the bundled advisory severity catalog")
    parser.add_argument("--cve", action="append", help="CVE to query and add; repeatable")
    parser.add_argument("--out", type=Path, default=OUTPUT_FILE, help="Output JSON path")
    args = parser.parse_args()

    existing = json.loads(args.out.read_text(encoding="utf-8")) if args.out.is_file() else []
    by_id: dict[str, dict] = {}
    for item in existing:
        row = {key: item[key] for key in ("id", "type", "vector", "source")}
        row["id"] = normalize_id(row["id"])
        by_id[row["id"]] = row

    failed = []
    for raw in args.cve or []:
        cve_id = normalize_id(raw)
        entry = fetch_cve_cvss(cve_id)
        if entry is None:
            failed.append(cve_id)
            continue
        by_id[cve_id] = entry
        print(f"Resolved {cve_id}: {entry['vector']}")

    out_list = sorted(by_id.values(), key=lambda row: row["id"])
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(out_list, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {len(out_list)} entries to {args.out}")
    if failed:
        print(f"No CVSS vector found for: {', '.join(failed)}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
