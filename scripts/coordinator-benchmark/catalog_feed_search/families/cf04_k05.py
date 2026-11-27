"""CF04 · K05: Scale and bounds — 50,000 and 500,000 file scalefixture trees (D8 serial scale seat)."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import threading
import time
import uuid

from ..driver_provenance import collect_run_provenance
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector
from .cf04_common import CF04SidecarClient, finalize_family_evidence, start_sidecar

FILES_PER_DIRECTORY = 500
DIRECTORIES_PER_GROUP = 50
SCALEFIXTURE_EXTENSIONS = (".go", ".ts", ".sql", ".py", ".md")


@dataclass(frozen=True)
class ScaleTreeShape:
    """Exact shape written by scalefixture-compatible generator."""

    files: int
    leaf_directories: int
    group_directories: int
    total_directories: int
    total_bytes: int


def scalefixture_path(index: int) -> str:
    """Exact match for lycaon/internal/scalefixture.Path(index)."""
    directory = index // FILES_PER_DIRECTORY
    return (
        f"g{directory // DIRECTORIES_PER_GROUP:03d}/"
        f"d{directory % DIRECTORIES_PER_GROUP:03d}/"
        f"f{index % FILES_PER_DIRECTORY:03d}{SCALEFIXTURE_EXTENSIONS[index % len(SCALEFIXTURE_EXTENSIONS)]}"
    )


def scalefixture_body(index: int) -> bytes:
    """Exact match for lycaon/internal/scalefixture.Body(index)."""
    head = f"// scale fixture file {index}\nfunc file{index}() int {{ return {index} }}\n"
    size = 128 + (index * 7919) % 384
    if len(head) >= size:
        return head[:size].encode("utf-8")
    return (head + ("x" * (size - len(head) - 1)) + "\n").encode("utf-8")


def write_scalefixture_tree(root: Path, files: int) -> ScaleTreeShape:
    """Materialize a deterministic scalefixture tree matching lycaon/internal/scalefixture.WriteTree."""
    if files < 1:
        raise ValueError(f"files must be >= 1, got {files}")
    root_path = Path(root)
    root_path.mkdir(parents=True, exist_ok=True)
    leaf_dirs = 0
    group_dirs: set[int] = set()
    total_bytes = 0

    for index in range(files):
        directory = index // FILES_PER_DIRECTORY
        group = directory // DIRECTORIES_PER_GROUP
        dir_in_group = directory % DIRECTORIES_PER_GROUP
        if index % FILES_PER_DIRECTORY == 0:
            dir_path = root_path / f"g{group:03d}" / f"d{dir_in_group:03d}"
            dir_path.mkdir(parents=True, exist_ok=True)
            leaf_dirs += 1
            group_dirs.add(group)
        else:
            dir_path = root_path / f"g{group:03d}" / f"d{dir_in_group:03d}"
        file_path = dir_path / f"f{index % FILES_PER_DIRECTORY:03d}{SCALEFIXTURE_EXTENSIONS[index % len(SCALEFIXTURE_EXTENSIONS)]}"
        body = scalefixture_body(index)
        file_path.write_bytes(body)
        total_bytes += len(body)

    return ScaleTreeShape(
        files=files,
        leaf_directories=leaf_dirs,
        group_directories=len(group_dirs),
        total_directories=1 + len(group_dirs) + leaf_dirs,
        total_bytes=total_bytes,
    )


def _run_k05_tree_instance(
    client: CF04SidecarClient,
    tree_dir: Path,
    other_project_id: str,
    other_root_id: str,
    *,
    name: str,
    selector: dict[str, object],
    shape: ScaleTreeShape,
    settle_timeout_s: float = 600.0,
) -> dict[str, object]:
    """Attach a scale tree, observe traversal progress, foreground browse, and other-project responsiveness."""
    defects: list[dict[str, object]] = []
    t0 = time.monotonic()

    proj = client.create_project([{"path": str(tree_dir.resolve()), "label": name, "is_primary": True}])
    project_id = str(proj["id"])
    root_id = str(proj["roots"][0]["id"])  # type: ignore[index]

    revisions_seen: list[int] = []
    indexed_progression: list[int] = []
    foreground_browse_during_walk: dict[str, object] | None = None
    other_project_during_walk: dict[str, object] | None = None
    admissions_probed = 0
    settled = False
    last_index: dict[str, object] = {}
    last_storage: dict[str, object] = {}

    deadline = time.monotonic() + settle_timeout_s
    waiter = threading.Event()
    while time.monotonic() < deadline:
        idx_resp = client.get_source_index(project_id)
        if idx_resp.status == 200 and isinstance(idx_resp.json_body, dict):
            last_index = idx_resp.json_body
            rev = int(last_index.get("revision") or 0)
            if not revisions_seen or revisions_seen[-1] != rev:
                revisions_seen.append(rev)
            cov_list = last_index.get("coverage") or []
            root_cov = next(
                (c for c in cov_list if isinstance(c, dict) and str(c.get("root_id") or "") == root_id),
                {},
            )
            complete = bool(root_cov.get("discovery_complete")) and str(root_cov.get("state") or "").lower() == "ready"
            if not complete:
                # Probe foreground browse during active traversal
                if foreground_browse_during_walk is None:
                    b_start = time.monotonic()
                    b_resp = client.browse_dir(project_id, root_id, ".")
                    b_ms = round((time.monotonic() - b_start) * 1000.0, 2)
                    entries = b_resp.json_body.get("entries") if isinstance(b_resp.json_body, dict) else []
                    foreground_browse_during_walk = {
                        "status": b_resp.status,
                        "entry_count": len(entries) if isinstance(entries, list) else 0,
                        "elapsed_ms": b_ms,
                        "during_state": root_cov.get("state"),
                        "during_discovery_complete": root_cov.get("discovery_complete"),
                    }
                    admissions_probed += 1
                # Probe other project's requests completing during large walk
                if other_project_during_walk is None:
                    o_start = time.monotonic()
                    o_browse = client.browse_dir(other_project_id, other_root_id, ".")
                    o_read = client.get_source_file(other_project_id, other_root_id, "ping.txt")
                    o_search = client.driver.search_raw("K05OtherProjectPingToken", origin_project_id=other_project_id, case_sensitive=True)
                    o_ms = round((time.monotonic() - o_start) * 1000.0, 2)
                    other_project_during_walk = {
                        "browse_status": o_browse.status,
                        "read_status": o_read.status,
                        "search_hit_count": len(o_search.get("hits") or []),
                        "elapsed_ms": o_ms,
                    }
                    admissions_probed += 1
            else:
                settled = True
                break

        st_resp = client.get_source_storage(project_id)
        if st_resp.status == 200 and isinstance(st_resp.json_body, dict):
            last_storage = st_resp.json_body
            inv = last_storage.get("inventory")
            if isinstance(inv, dict) and inv.get("indexed_files") is not None:
                n = int(inv["indexed_files"])
                if not indexed_progression or indexed_progression[-1] != n:
                    indexed_progression.append(n)
        waiter.wait(0.25)

    elapsed_s = round(time.monotonic() - t0, 3)

    # Fallback probes if walk settled faster than first poll
    if foreground_browse_during_walk is None:
        b_resp = client.browse_dir(project_id, root_id, ".")
        entries = b_resp.json_body.get("entries") if isinstance(b_resp.json_body, dict) else []
        foreground_browse_during_walk = {
            "status": b_resp.status,
            "entry_count": len(entries) if isinstance(entries, list) else 0,
            "settled_before_probe": True,
        }
        admissions_probed += 1
    if other_project_during_walk is None:
        o_browse = client.browse_dir(other_project_id, other_root_id, ".")
        o_read = client.get_source_file(other_project_id, other_root_id, "ping.txt")
        o_search = client.driver.search_raw("K05OtherProjectPingToken", origin_project_id=other_project_id, case_sensitive=True)
        other_project_during_walk = {
            "browse_status": o_browse.status,
            "read_status": o_read.status,
            "search_hit_count": len(o_search.get("hits") or []),
            "settled_before_probe": True,
        }
        admissions_probed += 1

    if not settled:
        defects.append({"code": "scale_walk_did_not_settle", "elapsed_s": elapsed_s, "last_index": last_index})
    if int(foreground_browse_during_walk.get("status", 0)) != 200 or int(foreground_browse_during_walk.get("entry_count", 0)) < shape.group_directories:
        defects.append({"code": "foreground_browse_failed", "probe": foreground_browse_during_walk})
    if (
        int(other_project_during_walk.get("browse_status", 0)) != 200
        or int(other_project_during_walk.get("read_status", 0)) != 200
        or int(other_project_during_walk.get("search_hit_count", 0)) < 1
    ):
        defects.append({"code": "other_project_unresponsive", "probe": other_project_during_walk})

    # Verify settled file lookup returns matches from the scale tree
    sample_lookup = client.driver.file_lookup(project_id, "f000.go", root_id=root_id, limit=25)
    sample_matches = len(sample_lookup.get("matches") or [])
    if sample_matches == 0:
        defects.append({"code": "settled_scale_lookup_empty", "sample_lookup": sample_lookup})

    client.delete_project_async(project_id, operation_id=str(uuid.uuid4()))

    return {
        "name": name,
        "selector": selector,
        "verdict": "PASS" if not defects else "DEFECT",
        "tree_shape": {
            "files": shape.files,
            "leaf_directories": shape.leaf_directories,
            "group_directories": shape.group_directories,
            "total_directories": shape.total_directories,
            "total_bytes": shape.total_bytes,
        },
        "settled": settled,
        "elapsed_s_diagnostic": elapsed_s,
        "published_revisions": revisions_seen,
        "published_batch_count": len(revisions_seen),
        "indexed_files_progression": indexed_progression,
        "admissions_probed": admissions_probed,
        "foreground_browse": foreground_browse_during_walk,
        "other_project_probe": other_project_during_walk,
        "sample_lookup_matches": sample_matches,
        "last_index_summary": last_index,
        "last_storage_summary": last_storage,
        "defects": defects,
    }


def run_k05_suite(
    *,
    engine_bin: Path,
    d1_worktree: Path,
    d1_commit: str,
    d1_tree: str,
    out_dir: Path,
    base_seed: str = BASE_SEED_PRIMARY,
    sizes: tuple[int, ...] = (50_000, 50_000, 500_000, 500_000),
    rg_bin: str = "rg",
    repo_root: Path | None = None,
) -> dict[str, object]:
    """Run K05 scale suite (4 generated instances: 2x50k + 2x500k, plus 2 hand-written edge checks).

    MUST ONLY BE EXECUTED UNDER D8 SERIAL SCALE SEAT APPROVAL FROM LEAD.
    """
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    harness_root = Path(repo_root).resolve() if repo_root else Path(__file__).resolve().parents[4]

    with tempfile.TemporaryDirectory(prefix="cf04-k05-") as tmp_str:
        tmp = Path(tmp_str)
        # Materialize one 50k tree and one 500k tree (reused across pairs in fresh sidecar/project attaches)
        unique_sizes = sorted(set(sizes))
        trees: dict[int, tuple[Path, ScaleTreeShape]] = {}
        for sz in unique_sizes:
            tdir = tmp / f"scale-{sz}"
            shape = write_scalefixture_tree(tdir, sz)
            trees[sz] = (tdir, shape)

        other_dir = tmp / "other-project"
        other_dir.mkdir(parents=True, exist_ok=True)
        (other_dir / "ping.txt").write_bytes(b"K05OtherProjectPingToken responsive\n")

        sidecar = start_sidecar(engine_bin, tmp / "sidecar", cwd=d1_worktree)
        try:
            launch_receipt = sidecar.launch_receipt()
            receipt_path = out / "launch_receipt.json"
            receipt_path.write_text(json.dumps(launch_receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")

            client = CF04SidecarClient(sidecar.url, sidecar.token)
            provenance = collect_run_provenance(
                harness_repo=harness_root,
                work_dir=tmp,
                binary_path=engine_bin,
                d1_commit=d1_commit,
                d1_tree=d1_tree,
                rg_bin=rg_bin,
            )

            # Attach the small second project for concurrent responsiveness verification
            other_proj = client.create_project([{"path": str(other_dir.resolve()), "label": "other", "is_primary": True}])
            other_pid = str(other_proj["id"])
            other_rid = str(other_proj["roots"][0]["id"])  # type: ignore[index]

            generated_results: list[dict[str, object]] = []
            for idx, sz in enumerate(sizes):
                sel = ReplaySelector(base_seed, "K05", idx)
                tdir, shape = trees[sz]
                res = _run_k05_tree_instance(
                    client,
                    tdir,
                    other_pid,
                    other_rid,
                    name=f"K05-inst-{idx:02d}-{sz}",
                    selector=sel.to_dict(),
                    shape=shape,
                )
                generated_results.append(res)

            # 2 Hand-written edge verdicts synthesized from direct scale probes:
            # K05-E01: Foreground directory listing publishes while traversal continues
            # K05-E02: Another project stays responsive while large traversal runs
            e1_ok = all(int(r["foreground_browse"].get("status", 0)) == 200 for r in generated_results)  # type: ignore[union-attr]
            e2_ok = all(
                int(r["other_project_probe"].get("browse_status", 0)) == 200  # type: ignore[union-attr]
                and int(r["other_project_probe"].get("read_status", 0)) == 200  # type: ignore[union-attr]
                and int(r["other_project_probe"].get("search_hit_count", 0)) >= 1  # type: ignore[union-attr]
                for r in generated_results
            )
            edge_results = [
                {
                    "name": "K05-E01-foreground-browse-during-walk",
                    "verdict": "PASS" if e1_ok else "DEFECT",
                    "probes": [r["foreground_browse"] for r in generated_results],
                },
                {
                    "name": "K05-E02-other-project-responsive-during-walk",
                    "verdict": "PASS" if e2_ok else "DEFECT",
                    "probes": [r["other_project_probe"] for r in generated_results],
                },
            ]

            gen_passed = sum(1 for r in generated_results if r["verdict"] == "PASS")
            gen_failed = len(generated_results) - gen_passed
            edge_passed = sum(1 for r in edge_results if r["verdict"] == "PASS")
            edge_failed = len(edge_results) - edge_passed

            report = {
                "family": "K05",
                "lane": "sidecar",
                "base_seed": base_seed,
                "sizes": list(sizes),
                "generated_planned": len(sizes),
                "generated_executed": len(generated_results),
                "generated_passed": gen_passed,
                "generated_failed": gen_failed,
                "edges_planned": 2,
                "edges_executed": len(edge_results),
                "edges_passed": edge_passed,
                "edges_failed": edge_failed,
                "generated_results": generated_results,
                "edge_results": edge_results,
            }
            report_path = out / "k05_results.json"
            report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")

            artifacts: dict[str, Path] = {
                "launch_receipt": receipt_path,
                "k05_results": report_path,
            }
            if sidecar.perf_file.is_file():
                perf_copy = out / "performance.jsonl"
                shutil.copy2(sidecar.perf_file, perf_copy)
                artifacts["performance_jsonl"] = perf_copy

            replay_cmd = [
                "python3",
                "-m",
                "catalog_feed_search.families.cf04_k05",
                "--engine-bin",
                str(engine_bin),
                "--d1-worktree",
                str(d1_worktree),
                "--d1-commit",
                d1_commit,
                "--d1-tree",
                d1_tree,
                "--seed",
                base_seed,
                "--sizes",
                ",".join(str(s) for s in sizes),
                "--rg-bin",
                rg_bin,
                "--out-dir",
                ".",
            ]
            finalize_family_evidence(
                out,
                provenance,
                artifacts,
                {
                    "family": "K05",
                    "sizes": list(sizes),
                    "generated_passed": gen_passed,
                    "generated_failed": gen_failed,
                    "edges_passed": edge_passed,
                    "edges_failed": edge_failed,
                },
                replay_command=replay_cmd,
                launch_receipt=launch_receipt,
            )
            return report
        finally:
            sidecar.stop()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run CF04 K05 (Scale trees — D8 serial scale seat required)")
    parser.add_argument("--engine-bin", required=True)
    parser.add_argument("--d1-worktree", required=True)
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="")
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY)
    parser.add_argument("--sizes", default="50000,50000,500000,500000")
    parser.add_argument("--rg-bin", default="rg")
    parser.add_argument("--repo-root", default="")
    args = parser.parse_args(argv)

    sizes = tuple(int(s.strip()) for s in args.sizes.split(",") if s.strip())
    report = run_k05_suite(
        engine_bin=Path(args.engine_bin),
        d1_worktree=Path(args.d1_worktree),
        d1_commit=args.d1_commit,
        d1_tree=args.d1_tree,
        out_dir=Path(args.out_dir),
        base_seed=args.seed,
        sizes=sizes,
        rg_bin=args.rg_bin,
        repo_root=Path(args.repo_root) if args.repo_root else None,
    )
    print(
        json.dumps(
            {
                "family": "K05",
                "sizes": report["sizes"],
                "generated_passed": report["generated_passed"],
                "generated_failed": report["generated_failed"],
                "edges_passed": report["edges_passed"],
                "edges_failed": report["edges_failed"],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
