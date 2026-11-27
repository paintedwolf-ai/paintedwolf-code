"""CLI runner for CF07 · Agent discovery (G01–G04 sidecar & evidence manifest)."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys
import tempfile

from ..driver_provenance import write_evidence_manifest
from ..generators import BoundaryConfig
from ..seeds import BASE_SEED_PRIMARY
from .cf07_g01 import run_g01_sidecar
from .cf07_g02 import run_g02_sidecar
from .cf07_g03 import run_g03_sidecar
from .cf07_g04 import run_g04_sidecar
from .cf07_harness import D1SidecarProcess

DEFAULT_COUNTS: dict[str, int] = {
    "G01": 200,
    "G02": 100,
    "G03": 40,
    "G04": 100,
}


def compute_gap_rate(family_report: dict[str, object]) -> float:
    executed = int(family_report.get("executed_instances") or 0)
    if executed <= 0:
        return 0.0
    counts = family_report.get("verdict_counts")
    if not isinstance(counts, dict):
        return 0.0
    gap_passes = int(counts.get("PASS_DECLARED_GAP") or 0)
    return round(gap_passes / executed, 6)


def write_sha256sums(out_dir: Path) -> Path:
    lines: list[str] = []
    for p in sorted(out_dir.rglob("*")):
        if not p.is_file() or p.name == "SHA256SUMS.txt":
            continue
        rel = p.relative_to(out_dir).as_posix()
        digest = hashlib.sha256(p.read_bytes()).hexdigest()
        lines.append(f"{digest}  {rel}")
    sums_path = out_dir / "SHA256SUMS.txt"
    sums_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return sums_path


def run_cf07_sidecar_suite(
    *,
    binary_path: Path,
    repo_root: Path,
    out_dir: Path,
    base_seed: str = BASE_SEED_PRIMARY,
    families: tuple[str, ...] = ("G01", "G02", "G03", "G04"),
    counts: dict[str, int] | None = None,
    rg_bin: str = "rg",
) -> dict[str, object]:
    """Execute CF07 sidecar families against pinned D1 binary and write retained evidence."""
    out_dir = Path(out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    effective_counts = dict(DEFAULT_COUNTS)
    if counts:
        effective_counts.update(counts)

    boundary_yaml = repo_root / "lycaon/config/runtime/source/directory-priority.yaml"
    boundary_cfg = BoundaryConfig.from_yaml_file(boundary_yaml) if boundary_yaml.is_file() else None

    family_reports: dict[str, dict[str, object]] = {}
    artifact_files: dict[str, Path] = {}

    with tempfile.TemporaryDirectory(prefix="cf07-sidecar-") as sidecar_tmp:
        with D1SidecarProcess(
            binary_path=binary_path,
            repo_root=repo_root,
            work_dir=Path(sidecar_tmp),
            rg_bin=rg_bin,
        ) as sidecar:
            assert sidecar.provenance is not None
            launch_path = out_dir / "launch_receipt.json"
            launch_path.write_text(json.dumps(sidecar.launch_receipt, indent=2) + "\n", encoding="utf-8")
            artifact_files["launch_receipt"] = launch_path

            for fam in families:
                n = effective_counts[fam]
                if fam == "G01":
                    rep = run_g01_sidecar(sidecar, base_seed=base_seed, instances=n, boundary_config=boundary_cfg)
                elif fam == "G02":
                    rep = run_g02_sidecar(sidecar, base_seed=base_seed, instances=n, boundary_config=boundary_cfg)
                elif fam == "G03":
                    rep = run_g03_sidecar(sidecar, base_seed=base_seed, instances=n)
                elif fam == "G04":
                    rep = run_g04_sidecar(sidecar, base_seed=base_seed, instances=n, rg_bin=rg_bin)
                else:
                    raise ValueError(f"unsupported CF07 family: {fam}")

                rep["declared_gap_rate"] = compute_gap_rate(rep)
                family_reports[fam] = rep
                fam_path = out_dir / f"{fam.lower()}_sidecar.json"
                fam_path.write_text(json.dumps(rep, indent=2) + "\n", encoding="utf-8")
                artifact_files[f"{fam.lower()}_sidecar"] = fam_path
                sys.stderr.write(
                    f"[CF07] finished {fam}: executed={rep['executed_instances']} verdicts={rep['verdict_counts']}\n"
                )
                sys.stderr.flush()

            summary = {
                "card": "CF07",
                "lane": "sidecar",
                "base_seed": base_seed,
                "provenance": sidecar.provenance.to_dict(),
                "families": {
                    fam: {
                        "planned_instances": rep["planned_instances"],
                        "executed_instances": rep["executed_instances"],
                        "verdict_counts": rep["verdict_counts"],
                        "declared_gap_counts": rep["declared_gap_counts"],
                        "declared_gap_rate": rep["declared_gap_rate"],
                        "defect_count": len(rep.get("defects") or []),
                        "edge_count": len(rep.get("edges") or []),
                    }
                    for fam, rep in family_reports.items()
                },
            }
            summary_path = out_dir / "summary.json"
            summary_path.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
            artifact_files["summary"] = summary_path

            replay_cmd = [
                sys.executable,
                "-m",
                "catalog_feed_search.families.cf07_runner",
                "--binary",
                str(binary_path),
                "--repo-root",
                str(repo_root),
                "--out-dir",
                str(out_dir),
                "--seed",
                base_seed,
                "--families",
                ",".join(families),
            ]
            write_evidence_manifest(
                out_dir,
                sidecar.provenance,
                artifact_files,
                extra={"card": "CF07", "base_seed": base_seed, "families": list(families)},
                replay_command=replay_cmd,
                launch_receipt=sidecar.launch_receipt,
            )
            write_sha256sums(out_dir)
            return summary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="CF07 Agent discovery sidecar runner")
    parser.add_argument("--binary", required=True, help="Pinned D1 lycaon-dev binary path")
    parser.add_argument("--repo-root", default=".", help="Repository root path")
    parser.add_argument("--out-dir", required=True, help="Output directory for CF07 evidence")
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY, help="Base seed (default 20261127)")
    parser.add_argument("--families", default="G01,G02,G03,G04", help="Comma-separated families")
    parser.add_argument("--g01-count", type=int, default=DEFAULT_COUNTS["G01"])
    parser.add_argument("--g02-count", type=int, default=DEFAULT_COUNTS["G02"])
    parser.add_argument("--g03-count", type=int, default=DEFAULT_COUNTS["G03"])
    parser.add_argument("--g04-count", type=int, default=DEFAULT_COUNTS["G04"])
    parser.add_argument("--rg-bin", default="rg")
    args = parser.parse_args(argv)

    fams = tuple(f.strip().upper() for f in args.families.split(",") if f.strip())
    summary = run_cf07_sidecar_suite(
        binary_path=Path(args.binary),
        repo_root=Path(args.repo_root),
        out_dir=Path(args.out_dir),
        base_seed=args.seed,
        families=fams,
        counts={
            "G01": args.g01_count,
            "G02": args.g02_count,
            "G03": args.g03_count,
            "G04": args.g04_count,
        },
        rg_bin=args.rg_bin,
    )
    print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
