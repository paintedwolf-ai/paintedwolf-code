"""CLI entry point for CF06 sidecar search-leg suite (S01-S08)."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
import tempfile
from typing import Sequence

from ..driver_provenance import collect_run_provenance, write_evidence_manifest
from ..driver_sidecar import HTTPTransport, SidecarDriver
from ..seeds import BASE_SEED_PRIMARY
from .cf06_code_legs import run_s01_sidecar, run_s02_sidecar, run_s03_sidecar
from .cf06_lookup_symbol_query import (
    run_s04_sidecar,
    run_s05_sidecar,
    run_s06_sidecar,
    run_s07_sidecar,
    run_s08_sidecar,
)
from .cf06_types import FamilySummary

LEDGER_SIDECAR_COUNTS: dict[str, int] = {
    "S01": 300,
    "S02": 200,
    "S03": 100,
    "S04": 200,
    "S05": 100,
    "S06": 200,
    "S07": 60,
    "S08": 60,
}


def run_cf06_sidecar_suite(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    families: tuple[str, ...] = tuple(LEDGER_SIDECAR_COUNTS.keys()),
    counts: dict[str, int] | None = None,
    s03_searches: int = 20,
    rg_bin: str = "rg",
    shrink_dir: Path | None = None,
) -> list[FamilySummary]:
    """Execute the requested CF06 sidecar families and return structured summaries."""
    eff = dict(LEDGER_SIDECAR_COUNTS)
    if counts:
        eff.update(counts)
    results: list[FamilySummary] = []
    for fam in families:
        n = eff[fam]
        if fam == "S01":
            results.append(run_s01_sidecar(driver, work_root, base_seed=base_seed, instances=n, rg_bin=rg_bin, shrink_dir=shrink_dir))
        elif fam == "S02":
            results.append(run_s02_sidecar(driver, work_root, base_seed=base_seed, instances=n, rg_bin=rg_bin))
        elif fam == "S03":
            results.append(run_s03_sidecar(driver, work_root, base_seed=base_seed, instances=n, searches_per_instance=s03_searches, rg_bin=rg_bin))
        elif fam == "S04":
            results.append(run_s04_sidecar(driver, work_root, base_seed=base_seed, instances=n))
        elif fam == "S05":
            results.append(run_s05_sidecar(driver, work_root, base_seed=base_seed, instances=n))
        elif fam == "S06":
            results.append(run_s06_sidecar(driver, work_root, base_seed=base_seed, instances=n))
        elif fam == "S07":
            results.append(run_s07_sidecar(driver, work_root, base_seed=base_seed, instances=n))
        elif fam == "S08":
            results.append(run_s08_sidecar(driver, work_root, base_seed=base_seed, instances=n))
        else:
            raise ValueError(f"unsupported CF06 family: {fam}")
    return results


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="cf06_cli")
    p.add_argument("--url", required=True, help="Sidecar base URL")
    p.add_argument("--token", required=True, help="Sidecar bearer auth token")
    p.add_argument("--out-dir", required=True, help="Directory for retained evidence")
    p.add_argument("--engine-bin", default="", help="Path to D1 lycaon-dev binary")
    p.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    p.add_argument("--d1-tree", default="")
    p.add_argument("--repo-root", default="")
    p.add_argument("--launch-receipt", default="")
    p.add_argument("--rg-bin", default="rg")
    p.add_argument("--seed", default=BASE_SEED_PRIMARY)
    p.add_argument("--families", default="S01,S02,S03,S04,S05,S06,S07,S08")
    p.add_argument("--scale", type=float, default=1.0, help="Scale factor for quick smoke runs (1.0 = full ledger)")
    return p


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path(__file__).resolve().parents[4]
    fams = tuple(f.strip().upper() for f in args.families.split(",") if f.strip())
    counts = {f: max(1, int(round(LEDGER_SIDECAR_COUNTS[f] * args.scale))) for f in fams}
    s03_searches = 20 if args.scale >= 1.0 else max(2, int(round(20 * args.scale)))

    transport = HTTPTransport(args.url, args.token)
    driver = SidecarDriver(transport)

    launch_receipt: dict[str, object] | None = None
    extra_artifacts: dict[str, Path] = {}
    if args.launch_receipt:
        lr_path = Path(args.launch_receipt).resolve()
        launch_receipt = json.loads(lr_path.read_text(encoding="utf-8"))
        extra_artifacts["launch_receipt"] = lr_path

    replay_cmd = [
        "python3",
        "-m",
        "catalog_feed_search.families.cf06_cli",
        "--url",
        args.url,
        "--token",
        "<REDACTED>",
        "--engine-bin",
        args.engine_bin,
        "--d1-commit",
        args.d1_commit,
        "--seed",
        args.seed,
        "--families",
        ",".join(fams),
        "--out-dir",
        ".",
        "--rg-bin",
        args.rg_bin,
    ]

    with tempfile.TemporaryDirectory() as tmp:
        work_root = Path(tmp) / "cf06_work"
        work_root.mkdir(parents=True, exist_ok=True)
        prov = collect_run_provenance(
            harness_repo=repo_root,
            work_dir=Path(tmp),
            binary_path=Path(args.engine_bin) if args.engine_bin else None,
            d1_commit=args.d1_commit,
            d1_tree=args.d1_tree,
            rg_bin=args.rg_bin,
        )
        shrink_dir = out_dir / "shrunk"
        summaries = run_cf06_sidecar_suite(
            driver,
            work_root,
            base_seed=args.seed,
            families=fams,
            counts=counts,
            s03_searches=s03_searches,
            rg_bin=args.rg_bin,
            shrink_dir=shrink_dir,
        )

    payload = {
        "card": "CF06",
        "lane": "sidecar",
        "base_seed": args.seed,
        "provenance": prov.to_dict(),
        "families": [s.to_dict() for s in summaries],
        "totals": {
            "planned": sum(s.planned for s in summaries),
            "executed": sum(s.executed for s in summaries),
            "passed": sum(s.passed for s in summaries),
            "failed": sum(s.failed for s in summaries),
            "blocked": sum(s.blocked for s in summaries),
            "inconclusive": sum(s.inconclusive for s in summaries),
            "edges_planned": sum(s.edges_planned for s in summaries),
            "edges_passed": sum(s.edges_passed for s in summaries),
            "edges_failed": sum(s.edges_failed for s in summaries),
        },
    }
    summary_path = out_dir / "cf06_sidecar_summary.json"
    summary_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    extra_artifacts["cf06_sidecar_summary"] = summary_path
    if shrink_dir.is_dir():
        for sp in sorted(shrink_dir.glob("*.json")):
            extra_artifacts[f"shrunk_{sp.stem}"] = sp

    write_evidence_manifest(
        out_dir,
        prov,
        extra_artifacts,
        extra=payload["totals"],
        replay_command=replay_cmd,
        launch_receipt=launch_receipt,
    )
    print(json.dumps(payload["totals"], indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
