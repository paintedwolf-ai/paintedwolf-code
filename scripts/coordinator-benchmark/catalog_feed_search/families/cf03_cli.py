"""CLI entry point for CF03 dual-host sidecar execution and evidence retention."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
import tempfile
from typing import Sequence

from ..driver_provenance import collect_run_provenance, write_evidence_manifest
from ..generators import load_repo_boundary_config
from ..seeds import BASE_SEED_PRIMARY
from .cf03_common import FamilyReport, ManagedSidecar, write_sha256sums
from .cf03_k01_k02 import K01_SIDECAR_COUNT, K02_SIDECAR_COUNT, run_k01_sidecar, run_k02_sidecar
from .cf03_k03_k04_k07 import (
    K03_SIDECAR_COUNT,
    K04_SIDECAR_COUNT,
    K07_SIDECAR_COUNT,
    run_k03_sidecar,
    run_k04_sidecar,
    run_k07_sidecar,
)

ALL_CF03_FAMILIES = ("K01", "K02", "K03", "K04", "K07")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="cf03_cli", description="Run CF03 sidecar families against a verified D1 binary.")
    parser.add_argument("--engine-bin", required=True, help="Path to verified D1 lycaon-dev binary")
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="a05c37487b1254b32be0640bcf47c0d52172f088")
    parser.add_argument("--out-dir", required=True, help="Evidence output directory")
    parser.add_argument("--repo-root", default="", help="Harness repo root (defaults to parent checkout)")
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY, help="Base seed (default 20261127)")
    parser.add_argument("--rg-bin", default="rg", help="Path to ripgrep 15.x executable")
    parser.add_argument(
        "--families",
        default=",".join(ALL_CF03_FAMILIES),
        help="Comma-separated subset of K01,K02,K03,K04,K07",
    )
    parser.add_argument("--k01-count", type=int, default=K01_SIDECAR_COUNT)
    parser.add_argument("--k02-count", type=int, default=K02_SIDECAR_COUNT)
    parser.add_argument("--k03-count", type=int, default=K03_SIDECAR_COUNT)
    parser.add_argument("--k04-count", type=int, default=K04_SIDECAR_COUNT)
    parser.add_argument("--k07-count", type=int, default=K07_SIDECAR_COUNT)
    return parser


def run_cf03_cli(args: argparse.Namespace) -> int:
    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path(__file__).resolve().parents[4]
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    boundary_cfg = load_repo_boundary_config(repo_root)
    selected = [f.strip().upper() for f in args.families.split(",") if f.strip()]

    reports: dict[str, FamilyReport] = {}
    incidents: list[dict[str, object]] = []
    artifact_files: dict[str, Path] = {}
    launch_receipts: dict[str, dict[str, object]] = {}
    first_launch_receipt: dict[str, object] = {}

    with tempfile.TemporaryDirectory(prefix="cf03-cfg-") as cfg_tmp:
        cfg_root = Path(cfg_tmp)
        provenance = collect_run_provenance(
            harness_repo=repo_root,
            work_dir=cfg_root,
            binary_path=Path(args.engine_bin),
            d1_commit=args.d1_commit,
            d1_tree=args.d1_tree,
            rg_bin=args.rg_bin,
        )

        for fam in selected:
            fam_cfg = cfg_root / fam.lower()
            with ManagedSidecar(Path(args.engine_bin), fam_cfg, repo_root=repo_root, startup_timeout_s=90.0) as sidecar:
                lr = {
                    "family": fam,
                    "utc_started": sidecar.started_utc,
                    "pid": sidecar.proc.pid if sidecar.proc else 0,
                    "url": sidecar.url,
                    "binary_path": str(sidecar.binary_path),
                    "binary_sha256": provenance.product.binary_sha256,
                    "d1_commit": args.d1_commit,
                    "d1_tree": args.d1_tree,
                    "harness_commit": provenance.harness_commit,
                    "harness_tree": provenance.harness_tree,
                }
                launch_receipts[fam] = lr
                if not first_launch_receipt:
                    first_launch_receipt = lr

                if fam == "K01":
                    reports["K01"] = run_k01_sidecar(sidecar.driver(), boundary_cfg, out_dir, base_seed=args.seed, count=args.k01_count)
                elif fam == "K02":
                    r_k02, inc_k02 = run_k02_sidecar(sidecar.driver(), boundary_cfg, out_dir, base_seed=args.seed, count=args.k02_count)
                    reports["K02"] = r_k02
                    incidents.extend(inc_k02)
                elif fam == "K03":
                    reports["K03"] = run_k03_sidecar(sidecar, boundary_cfg, base_seed=args.seed, count=args.k03_count)
                elif fam == "K04":
                    reports["K04"] = run_k04_sidecar(sidecar.driver(), boundary_cfg, base_seed=args.seed, count=args.k04_count)
                elif fam == "K07":
                    reports["K07"] = run_k07_sidecar(sidecar.driver(), fam_cfg, base_seed=args.seed, count=args.k07_count)

                if fam in reports:
                    fpath = out_dir / f"{fam.lower()}_sidecar.json"
                    fpath.write_text(json.dumps(reports[fam].to_dict(), indent=2, sort_keys=True) + "\n", encoding="utf-8")
                    artifact_files[f"{fam.lower()}_sidecar"] = fpath

        launch_receipt = dict(first_launch_receipt)
        launch_receipt["per_family_receipts"] = launch_receipts
        lr_path = out_dir / "launch_receipt.json"
        lr_path.write_text(json.dumps(launch_receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        artifact_files["launch_receipt"] = lr_path

    if incidents:
        inc_path = out_dir / "k02_incidents.json"
        inc_path.write_text(json.dumps(incidents, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        artifact_files["k02_incidents"] = inc_path

    all_ok = all(r.failed == 0 and r.edge_failed == 0 and r.inconclusive == 0 for r in reports.values()) and all(
        i.get("verdict") != "DEFECT" for i in incidents
    )
    summary = {
        "card": "CF03",
        "lane": "sidecar",
        "base_seed": args.seed,
        "all_passed": all_ok,
        "provenance": provenance.to_dict(),
        "families": {fam: rep.to_dict() for fam, rep in sorted(reports.items())},
        "incidents": incidents,
    }
    summary_path = out_dir / "cf03_sidecar_summary.json"
    summary_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    artifact_files["cf03_sidecar_summary"] = summary_path

    replay_cmd = [
        "python3",
        "-m",
        "catalog_feed_search.families.cf03_cli",
        "--engine-bin",
        args.engine_bin,
        "--d1-commit",
        args.d1_commit,
        "--d1-tree",
        args.d1_tree,
        "--seed",
        args.seed,
        "--families",
        ",".join(selected),
        "--out-dir",
        ".",
    ]
    write_evidence_manifest(
        out_dir,
        provenance,
        artifact_files,
        extra={"all_passed": all_ok, "families": selected},
        replay_command=replay_cmd,
        launch_receipt=launch_receipt,
    )
    write_sha256sums(out_dir)
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if all_ok else 1


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return run_cf03_cli(args)


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
