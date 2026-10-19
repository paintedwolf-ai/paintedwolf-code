"""Refuse Linux release admission without the exact complete installed campaign."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys

from .installed_results import admit
from .support_matrix import CATALOG


def document(path: Path) -> tuple[object, str]:
    with path.open("rb") as source:
        data = source.read((64 << 20) + 1)
    if len(data) > 64 << 20:
        raise ValueError("release admission input exceeds its size bound")
    return json.loads(data), hashlib.sha256(data).hexdigest()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--final-artifacts", type=Path, required=True)
    parser.add_argument("--campaign", type=Path, required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args(argv)
    evidence = args.evidence_root.resolve(strict=True)
    output = args.receipt.resolve()
    if output.is_relative_to(evidence) or output in {
            args.catalog.resolve(), args.final_artifacts.resolve(), args.campaign.resolve()}:
        parser.error("admission receipt must not replace its inputs or retained evidence")
    inputs = {}
    try:
        catalog, inputs["catalog_sha256"] = document(args.catalog)
        artifacts, inputs["final_artifacts_sha256"] = document(args.final_artifacts)
        campaign, inputs["campaign_sha256"] = document(args.campaign)
        result = admit(catalog, campaign, artifacts, args.source_commit, evidence)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        result = {"accepted": False, "errors": [str(exc)]}
    report = {"format_version": 1, "source_commit": args.source_commit,
              "inputs": inputs, "accepted": result["accepted"], "result": result}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n")
    if not report["accepted"]:
        print(f"Linux release admission refused: {output}", file=sys.stderr)
        return 1
    print(f"Linux installed campaign admitted: {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
