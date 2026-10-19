"""Refuse Linux release admission without the exact complete installed campaign."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys

from .installed_results import admit
from .publication_files import verify_files
from .publication_repositories import verify_repositories
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
    parser.add_argument("--file-root", type=Path, required=True)
    parser.add_argument("--repositories", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--channel", required=True)
    parser.add_argument("--signing-generation", type=int, required=True)
    parser.add_argument("--key-fingerprint", required=True)
    parser.add_argument("--campaign", type=Path, required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args(argv)
    evidence = args.evidence_root.resolve(strict=True)
    output = args.receipt.resolve()
    if output.is_relative_to(args.file_root.resolve()) or output.is_relative_to(evidence) or output in {
            args.catalog.resolve(), args.final_artifacts.resolve(), args.campaign.resolve(), args.repositories.resolve()}:
        parser.error("admission receipt must not replace its inputs or retained evidence")
    inputs = {}
    verified_files = []
    verified_repositories = []
    try:
        catalog, inputs["catalog_sha256"] = document(args.catalog)
        artifacts, inputs["final_artifacts_sha256"] = document(args.final_artifacts)
        campaign, inputs["campaign_sha256"] = document(args.campaign)
        repositories, inputs["repositories_sha256"] = document(args.repositories)
        result = admit(catalog, campaign, artifacts, args.source_commit, evidence)
        if result["accepted"]:
            verified_files = verify_files(artifacts, args.source_commit, args.file_root)
            verified_repositories = verify_repositories(
                repositories, artifacts, args.source_commit, version=args.version, channel=args.channel,
                signing_generation=args.signing_generation, key_fingerprint=args.key_fingerprint,
                verified_files=verified_files)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        result = {"accepted": False, "errors": [str(exc)]}
    report = {"format_version": 1, "source_commit": args.source_commit,
              "inputs": inputs, "accepted": result["accepted"], "result": result,
              "verified_files": verified_files, "verified_repositories": verified_repositories}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n")
    if not report["accepted"]:
        print(f"Linux release admission refused: {output}", file=sys.stderr)
        return 1
    print(f"Linux installed campaign admitted: {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
