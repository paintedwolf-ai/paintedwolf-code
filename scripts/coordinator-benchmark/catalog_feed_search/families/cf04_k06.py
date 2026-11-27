"""CF04 · K06: Roots, checkouts and worker branches (100 sidecar instances + 5 hand-written edges)."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

from ..driver_provenance import collect_run_provenance
from ..live_controls import wait_catalog_settled
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from .cf04_common import (
    CF04SidecarClient,
    finalize_family_evidence,
    seed_worker_branch_in_db,
    sha256_bytes,
    start_sidecar,
)


@dataclass(frozen=True)
class K06InstanceSpec:
    """Generated root-set & worker-branch topology for one K06 instance."""

    selector: ReplaySelector
    root_count: int
    worker_count: int
    include_two_checkouts: bool
    include_nested_root: bool
    perform_label_rename: bool
    perform_primary_swap: bool
    test_retired_worker: bool


def generate_k06_spec(base_seed: str, instance_index: int) -> K06InstanceSpec:
    """Deterministically generate one K06 root set (1-4 roots, 0-2 worker branches)."""
    sel = ReplaySelector(base_seed, "K06", instance_index)
    rng = seed_rng(base_seed, "K06", instance_index)
    root_count = rng.randint(1, 4)
    worker_count = rng.randint(0, 2)
    include_two_checkouts = root_count >= 2 and rng.random() < 0.35
    include_nested_root = root_count >= 2 and (not include_two_checkouts) and rng.random() < 0.35
    perform_label_rename = rng.random() < 0.65
    perform_primary_swap = root_count >= 2 and rng.random() < 0.5
    test_retired_worker = worker_count >= 1 or (rng.random() < 0.4)
    return K06InstanceSpec(
        selector=sel,
        root_count=root_count,
        worker_count=worker_count,
        include_two_checkouts=include_two_checkouts,
        include_nested_root=include_nested_root,
        perform_label_rename=perform_label_rename,
        perform_primary_swap=perform_primary_swap,
        test_retired_worker=test_retired_worker,
    )


def _init_git_repo_and_clone(base_dir: Path, checkout_a: Path, checkout_b: Path, token: str) -> None:
    """Create a real bare git repo and clone two independent checkouts of it."""
    bare = base_dir / "origin.git"
    subprocess.run(["git", "init", "--bare", str(bare)], check=True, capture_output=True)
    subprocess.run(["git", "clone", str(bare), str(checkout_a)], check=True, capture_output=True)
    subprocess.run(["git", "-C", str(checkout_a), "config", "user.email", "cf04@example.invalid"], check=True)
    subprocess.run(["git", "-C", str(checkout_a), "config", "user.name", "CF04"], check=True)
    (checkout_a / "shared.txt").write_bytes(f"repo-shared {token} checkout-a\n".encode("utf-8"))
    subprocess.run(["git", "-C", str(checkout_a), "add", "shared.txt"], check=True, capture_output=True)
    subprocess.run(["git", "-C", str(checkout_a), "commit", "-m", "initial"], check=True, capture_output=True)
    subprocess.run(["git", "-C", str(checkout_a), "push", "origin", "HEAD"], check=True, capture_output=True)
    subprocess.run(["git", "clone", str(bare), str(checkout_b)], check=True, capture_output=True)
    (checkout_b / "shared.txt").write_bytes(f"repo-shared {token} checkout-b\n".encode("utf-8"))


def _verify_k06_scenario(
    client: CF04SidecarClient,
    db_path: Path,
    work_dir: Path,
    *,
    name: str,
    selector: dict[str, object],
    root_dirs: list[tuple[str, Path, bytes]],
    active_worker_count: int,
    rename_root_idx: int | None,
    swap_primary_idx: int | None,
    check_retired_worker: bool,
    search_token: str,
) -> dict[str, object]:
    """Execute one K06 scenario against a real D1 sidecar and return structured verdict + evidence."""
    defects: list[dict[str, object]] = []
    # Attach initial root via POST /v1/projects, then attach remaining roots via POST /v1/projects/{id}/roots
    first_label, first_path, _ = root_dirs[0]
    proj = client.create_project([{"path": str(first_path.resolve()), "label": first_label, "is_primary": True}])
    project_id = str(proj["id"])
    root_ids_by_path: dict[str, str] = {}
    for r in proj.get("roots") or []:
        if isinstance(r, dict) and r.get("id") and r.get("path"):
            root_ids_by_path[str(Path(str(r["path"])).resolve())] = str(r["id"])

    initial_ids: list[str] = [root_ids_by_path[str(first_path.resolve())]]
    for label, rpath, _ in root_dirs[1:]:
        resp = client.attach_root(project_id, rpath, label=label)
        if resp.status not in (200, 201) or not isinstance(resp.json_body, dict):
            defects.append(
                {
                    "code": "root_attach_failed",
                    "detail": f"attach {label} returned status={resp.status}: {resp.body_text[:200]}",
                }
            )
            continue
        for r in resp.json_body.get("roots") or []:
            if isinstance(r, dict) and r.get("id") and r.get("path"):
                resolved = str(Path(str(r["path"])).resolve())
                prev = root_ids_by_path.get(resolved)
                cur = str(r["id"])
                if prev is not None and prev != cur:
                    defects.append(
                        {
                            "code": "root_id_changed_on_attach",
                            "path": resolved,
                            "before": prev,
                            "after": cur,
                        }
                    )
                root_ids_by_path[resolved] = cur
        rid = root_ids_by_path.get(str(rpath.resolve()))
        if rid:
            initial_ids.append(rid)

    # Check distinct root IDs for distinct root paths
    if len(set(initial_ids)) != len(initial_ids):
        defects.append({"code": "duplicate_root_ids", "root_ids": initial_ids})

    # Optional label rename & primary root swap — root IDs must remain identical
    if rename_root_idx is not None and rename_root_idx < len(initial_ids):
        target_rid = initial_ids[rename_root_idx]
        upd = client.update_root(project_id, target_rid, label=f"renamed-{name}-{rename_root_idx}")
        if upd.status != 200 or not isinstance(upd.json_body, dict):
            defects.append({"code": "root_rename_failed", "status": upd.status, "body": upd.body_text[:200]})
        else:
            after_map = {
                str(Path(str(r["path"])).resolve()): str(r["id"])
                for r in (upd.json_body.get("roots") or [])
                if isinstance(r, dict) and r.get("id") and r.get("path")
            }
            for p_str, orig_id in root_ids_by_path.items():
                if after_map.get(p_str) != orig_id:
                    defects.append(
                        {
                            "code": "root_id_changed_on_rename",
                            "path": p_str,
                            "before": orig_id,
                            "after": after_map.get(p_str),
                        }
                    )

    if swap_primary_idx is not None and 0 < swap_primary_idx < len(initial_ids):
        target_rid = initial_ids[swap_primary_idx]
        upd = client.update_root(project_id, target_rid, is_primary=True)
        if upd.status != 200 or not isinstance(upd.json_body, dict):
            defects.append({"code": "root_primary_swap_failed", "status": upd.status, "body": upd.body_text[:200]})
        else:
            after_map = {
                str(Path(str(r["path"])).resolve()): str(r["id"])
                for r in (upd.json_body.get("roots") or [])
                if isinstance(r, dict) and r.get("id") and r.get("path")
            }
            for p_str, orig_id in root_ids_by_path.items():
                if after_map.get(p_str) != orig_id:
                    defects.append(
                        {
                            "code": "root_id_changed_on_primary_swap",
                            "path": p_str,
                            "before": orig_id,
                            "after": after_map.get(p_str),
                        }
                    )

    # Settle catalog for all attached roots
    for rid in initial_ids:
        wait_catalog_settled(client.driver, project_id, rid, max_wait_s=10.0, poll_interval_s=0.05)

    # 1. Verify root IDs in /source/workspace
    ws_resp = client.get_source_workspace(project_id)
    ws_root_ids: set[str] = set()
    if ws_resp.status == 200 and isinstance(ws_resp.json_body, dict):
        for r in ws_resp.json_body.get("roots") or []:
            if isinstance(r, dict) and r.get("id"):
                ws_root_ids.add(str(r["id"]))
    if set(initial_ids) != ws_root_ids:
        defects.append(
            {
                "code": "workspace_root_id_mismatch",
                "expected": sorted(initial_ids),
                "actual": sorted(ws_root_ids),
            }
        )

    # 2. Verify root IDs in search hits & export
    search_resp = client.driver.search_raw(search_token, origin_project_id=project_id, case_sensitive=True, limit=200)
    hit_root_ids = {
        str(h.get("root_id") or "")
        for h in (search_resp.get("hits") or [])
        if isinstance(h, dict) and h.get("root_id")
    }
    if not hit_root_ids.issubset(set(initial_ids)):
        defects.append(
            {
                "code": "search_hit_unknown_root_id",
                "expected_subset_of": sorted(initial_ids),
                "actual": sorted(hit_root_ids),
            }
        )
    if not hit_root_ids:
        defects.append({"code": "search_hits_empty", "token": search_token})

    export_text, _ = client.driver.export_search(search_token, fmt="jsonl", origin_project_id=project_id, case_sensitive=True)
    export_root_ids: set[str] = set()
    for line in export_text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if isinstance(row, dict) and row.get("root_id"):
            export_root_ids.add(str(row["root_id"]))
    if export_root_ids and not export_root_ids.issubset(set(initial_ids)):
        defects.append(
            {
                "code": "export_unknown_root_id",
                "expected_subset_of": sorted(initial_ids),
                "actual": sorted(export_root_ids),
            }
        )

    # 3. File activation (GET /v1/projects/{id}/source) per root — verify root_id and content SHA-256
    for idx, (_, rpath, expected_bytes) in enumerate(root_dirs):
        rid = root_ids_by_path.get(str(rpath.resolve()))
        if not rid:
            continue
        file_resp = client.get_source_file(project_id, rid, "target.txt")
        if file_resp.status != 200 or not isinstance(file_resp.json_body, dict):
            defects.append(
                {
                    "code": "trunk_file_activation_failed",
                    "root_id": rid,
                    "status": file_resp.status,
                    "body": file_resp.body_text[:200],
                }
            )
            continue
        got_rid = str(file_resp.json_body.get("root_id") or "")
        got_sha = str(file_resp.json_body.get("sha256") or "")
        got_content = str(file_resp.json_body.get("content") or "").encode("utf-8")
        want_sha = sha256_bytes(expected_bytes)
        if got_rid != rid:
            defects.append({"code": "file_activation_root_id_mismatch", "expected": rid, "actual": got_rid})
        if got_sha != want_sha or sha256_bytes(got_content) != want_sha:
            defects.append(
                {
                    "code": "file_activation_hash_mismatch",
                    "root_id": rid,
                    "expected_sha256": want_sha,
                    "actual_sha256": got_sha,
                    "content_sha256": sha256_bytes(got_content),
                }
            )

    # 4. Active worker branch reads (must read worker's bytes by hash, workspace_kind=="worker")
    primary_rid = initial_ids[0]
    primary_path = root_dirs[0][1]
    primary_bytes = root_dirs[0][2]
    roots_meta = [
        {
            "id": root_ids_by_path[str(rp.resolve())],
            "path": str(rp.resolve()),
            "label": lbl,
            "is_primary": (i == 0),
        }
        for i, (lbl, rp, _) in enumerate(root_dirs)
        if str(rp.resolve()) in root_ids_by_path
    ]
    worker_results: list[dict[str, object]] = []
    for w_idx in range(active_worker_count):
        worker_id = str(uuid.uuid4())
        worker_bytes = f"worker-{name}-{w_idx}-bytes {search_token} unique-{worker_id}\n".encode("utf-8")
        worker_sha = sha256_bytes(worker_bytes)
        seed_worker_branch_in_db(
            db_path,
            worker_id=worker_id,
            project_id=project_id,
            primary_root_id=primary_rid,
            primary_root_path=primary_path,
            branch_files_by_root={primary_rid: {"target.txt": worker_bytes}},
            roots_meta=roots_meta,
            status="complete",
            merge_status="pending",
            retired=False,
        )
        w_resp = client.get_source_file(project_id, primary_rid, "target.txt", worker_id=worker_id)
        got_kind = str(w_resp.json_body.get("workspace_kind") or "") if isinstance(w_resp.json_body, dict) else ""
        got_sha = str(w_resp.json_body.get("sha256") or "") if isinstance(w_resp.json_body, dict) else ""
        got_bytes = str(w_resp.json_body.get("content") or "").encode("utf-8") if isinstance(w_resp.json_body, dict) else b""
        worker_results.append(
            {
                "worker_id": worker_id,
                "status": w_resp.status,
                "workspace_kind": got_kind,
                "expected_sha256": worker_sha,
                "actual_sha256": got_sha,
            }
        )
        if w_resp.status != 200 or got_kind != "worker" or got_sha != worker_sha or sha256_bytes(got_bytes) != worker_sha:
            defects.append(
                {
                    "code": "worker_branch_read_mismatch",
                    "worker_id": worker_id,
                    "status": w_resp.status,
                    "workspace_kind": got_kind,
                    "expected_sha256": worker_sha,
                    "actual_sha256": got_sha,
                    "trunk_sha256": sha256_bytes(primary_bytes),
                }
            )

    # 5. Retired worker workspace read — contract requires explicit unavailable state, NEVER silent trunk fallback
    retired_result: dict[str, object] | None = None
    if check_retired_worker:
        retired_id = str(uuid.uuid4())
        seed_worker_branch_in_db(
            db_path,
            worker_id=retired_id,
            project_id=project_id,
            primary_root_id=primary_rid,
            primary_root_path=primary_path,
            branch_files_by_root=None,
            roots_meta=roots_meta,
            status="complete",
            merge_status="merged",
            retired=True,
        )
        r_resp = client.get_source_file(project_id, primary_rid, "target.txt", worker_id=retired_id)
        r_kind = str(r_resp.json_body.get("workspace_kind") or "") if isinstance(r_resp.json_body, dict) else ""
        r_sha = str(r_resp.json_body.get("sha256") or "") if isinstance(r_resp.json_body, dict) else ""
        retired_result = {
            "retired_worker_id": retired_id,
            "status": r_resp.status,
            "workspace_kind": r_kind,
            "returned_sha256": r_sha,
            "trunk_sha256": sha256_bytes(primary_bytes),
            "body_snippet": r_resp.body_text[:300],
        }
        # If HTTP 200 returned trunk bytes with workspace_kind="project", that is a defect (silent fallback)!
        if r_resp.status == 200:
            defects.append(
                {
                    "code": "retired_worker_silent_trunk_fallback",
                    "detail": "GET /v1/projects/{id}/source?worker_id=<retired> returned HTTP 200 with trunk bytes instead of explicit unavailable state",
                    "retired_worker_id": retired_id,
                    "workspace_kind": r_kind,
                    "returned_sha256": r_sha,
                    "trunk_sha256": sha256_bytes(primary_bytes),
                }
            )

    # Clean up project asynchronously to keep sidecar tidy
    client.delete_project_async(project_id, operation_id=str(uuid.uuid4()))

    return {
        "name": name,
        "selector": selector,
        "verdict": "PASS" if not defects else "DEFECT",
        "root_count": len(root_dirs),
        "root_ids": initial_ids,
        "hit_root_ids": sorted(hit_root_ids),
        "export_root_ids": sorted(export_root_ids),
        "worker_results": worker_results,
        "retired_worker_result": retired_result,
        "defects": defects,
    }


def run_k06_suite(
    *,
    engine_bin: Path,
    d1_worktree: Path,
    d1_commit: str,
    d1_tree: str,
    out_dir: Path,
    base_seed: str = BASE_SEED_PRIMARY,
    instance_count: int = 100,
    rg_bin: str = "rg",
    repo_root: Path | None = None,
) -> dict[str, object]:
    """Run all K06 generated sidecar instances (default 100) + 5 hand-written edge slots."""
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    harness_root = Path(repo_root).resolve() if repo_root else Path(__file__).resolve().parents[4]

    with tempfile.TemporaryDirectory(prefix="cf04-k06-") as tmp_str:
        tmp = Path(tmp_str)
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

            generated_results: list[dict[str, object]] = []
            for idx in range(instance_count):
                spec = generate_k06_spec(base_seed, idx)
                inst_dir = tmp / f"inst-{idx:03d}"
                inst_dir.mkdir(parents=True, exist_ok=True)
                token = f"K06Tok{idx:03d}Seed{base_seed}"
                root_dirs: list[tuple[str, Path, bytes]] = []

                if spec.include_two_checkouts:
                    co_a = inst_dir / "checkout-a"
                    co_b = inst_dir / "checkout-b"
                    _init_git_repo_and_clone(inst_dir, co_a, co_b, token)
                    data_a = f"root-0 {token} checkout-a\n".encode("utf-8")
                    data_b = f"root-1 {token} checkout-b\n".encode("utf-8")
                    (co_a / "target.txt").write_bytes(data_a)
                    (co_b / "target.txt").write_bytes(data_b)
                    root_dirs.append(("checkout-a", co_a, data_a))
                    root_dirs.append(("checkout-b", co_b, data_b))
                    for r_idx in range(2, spec.root_count):
                        rp = inst_dir / f"extra-{r_idx}"
                        rp.mkdir(parents=True, exist_ok=True)
                        data = f"root-{r_idx} {token} extra\n".encode("utf-8")
                        (rp / "target.txt").write_bytes(data)
                        root_dirs.append((f"extra-{r_idx}", rp, data))
                elif spec.include_nested_root:
                    parent = inst_dir / "parent"
                    child = parent / "nested-child"
                    child.mkdir(parents=True, exist_ok=True)
                    data_p = f"root-0 {token} parent\n".encode("utf-8")
                    data_c = f"root-1 {token} child\n".encode("utf-8")
                    (parent / "target.txt").write_bytes(data_p)
                    (child / "target.txt").write_bytes(data_c)
                    root_dirs.append(("parent", parent, data_p))
                    root_dirs.append(("nested-child", child, data_c))
                    for r_idx in range(2, spec.root_count):
                        rp = inst_dir / f"sibling-{r_idx}"
                        rp.mkdir(parents=True, exist_ok=True)
                        data = f"root-{r_idx} {token} sibling\n".encode("utf-8")
                        (rp / "target.txt").write_bytes(data)
                        root_dirs.append((f"sibling-{r_idx}", rp, data))
                else:
                    for r_idx in range(spec.root_count):
                        rp = inst_dir / f"root-{r_idx}"
                        rp.mkdir(parents=True, exist_ok=True)
                        data = f"root-{r_idx} {token} plain\n".encode("utf-8")
                        (rp / "target.txt").write_bytes(data)
                        root_dirs.append((f"root-{r_idx}", rp, data))

                res = _verify_k06_scenario(
                    client,
                    sidecar.db_path,
                    inst_dir,
                    name=f"K06-inst-{idx:03d}",
                    selector=spec.selector.to_dict(),
                    root_dirs=root_dirs,
                    active_worker_count=spec.worker_count,
                    rename_root_idx=0 if spec.perform_label_rename else None,
                    swap_primary_idx=1 if spec.perform_primary_swap else None,
                    check_retired_worker=spec.test_retired_worker,
                    search_token=token,
                )
                generated_results.append(res)
                shutil.rmtree(inst_dir, ignore_errors=True)

            # 5 Hand-written edge slots
            edge_results: list[dict[str, object]] = []

            # K06-E01: Two checkouts of one repository
            e1_dir = tmp / "edge-e01"
            e1_dir.mkdir(parents=True, exist_ok=True)
            e1_a, e1_b = e1_dir / "co-a", e1_dir / "co-b"
            _init_git_repo_and_clone(e1_dir, e1_a, e1_b, "K06EdgeE01Token")
            e1_ba, e1_bb = b"K06EdgeE01Token checkout-a\n", b"K06EdgeE01Token checkout-b\n"
            (e1_a / "target.txt").write_bytes(e1_ba)
            (e1_b / "target.txt").write_bytes(e1_bb)
            edge_results.append(
                _verify_k06_scenario(
                    client,
                    sidecar.db_path,
                    e1_dir,
                    name="K06-E01-two-checkouts",
                    selector={"edge": "K06-E01", "base_seed": base_seed},
                    root_dirs=[("checkout-a", e1_a, e1_ba), ("checkout-b", e1_b, e1_bb)],
                    active_worker_count=0,
                    rename_root_idx=None,
                    swap_primary_idx=None,
                    check_retired_worker=False,
                    search_token="K06EdgeE01Token",
                )
            )

            # K06-E02: Nested root inside another root
            e2_dir = tmp / "edge-e02"
            e2_p, e2_c = e2_dir / "outer", e2_dir / "outer" / "inner"
            e2_c.mkdir(parents=True, exist_ok=True)
            e2_bp, e2_bc = b"K06EdgeE02Token outer\n", b"K06EdgeE02Token inner\n"
            (e2_p / "target.txt").write_bytes(e2_bp)
            (e2_c / "target.txt").write_bytes(e2_bc)
            edge_results.append(
                _verify_k06_scenario(
                    client,
                    sidecar.db_path,
                    e2_dir,
                    name="K06-E02-nested-roots",
                    selector={"edge": "K06-E02", "base_seed": base_seed},
                    root_dirs=[("outer", e2_p, e2_bp), ("inner", e2_c, e2_bc)],
                    active_worker_count=0,
                    rename_root_idx=None,
                    swap_primary_idx=None,
                    check_retired_worker=False,
                    search_token="K06EdgeE02Token",
                )
            )

            # K06-E03: Root rename + primary swap stability across hits/export/activation
            e3_dir = tmp / "edge-e03"
            e3_r0, e3_r1 = e3_dir / "r0", e3_dir / "r1"
            e3_r0.mkdir(parents=True, exist_ok=True)
            e3_r1.mkdir(parents=True, exist_ok=True)
            e3_b0, e3_b1 = b"K06EdgeE03Token r0\n", b"K06EdgeE03Token r1\n"
            (e3_r0 / "target.txt").write_bytes(e3_b0)
            (e3_r1 / "target.txt").write_bytes(e3_b1)
            edge_results.append(
                _verify_k06_scenario(
                    client,
                    sidecar.db_path,
                    e3_dir,
                    name="K06-E03-rename-and-primary-swap",
                    selector={"edge": "K06-E03", "base_seed": base_seed},
                    root_dirs=[("r0", e3_r0, e3_b0), ("r1", e3_r1, e3_b1)],
                    active_worker_count=0,
                    rename_root_idx=0,
                    swap_primary_idx=1,
                    check_retired_worker=False,
                    search_token="K06EdgeE03Token",
                )
            )

            # K06-E04: Active worker branch reads worker bytes by hash
            e4_dir = tmp / "edge-e04"
            e4_r0 = e4_dir / "r0"
            e4_r0.mkdir(parents=True, exist_ok=True)
            e4_b0 = b"K06EdgeE04Token trunk-bytes\n"
            (e4_r0 / "target.txt").write_bytes(e4_b0)
            edge_results.append(
                _verify_k06_scenario(
                    client,
                    sidecar.db_path,
                    e4_dir,
                    name="K06-E04-active-worker-branch-hash",
                    selector={"edge": "K06-E04", "base_seed": base_seed},
                    root_dirs=[("r0", e4_r0, e4_b0)],
                    active_worker_count=2,
                    rename_root_idx=None,
                    swap_primary_idx=None,
                    check_retired_worker=False,
                    search_token="K06EdgeE04Token",
                )
            )

            # K06-E05: Retired worker workspace resolves to explicit unavailable state
            e5_dir = tmp / "edge-e05"
            e5_r0 = e5_dir / "r0"
            e5_r0.mkdir(parents=True, exist_ok=True)
            e5_b0 = b"K06EdgeE05Token trunk-only\n"
            (e5_r0 / "target.txt").write_bytes(e5_b0)
            edge_results.append(
                _verify_k06_scenario(
                    client,
                    sidecar.db_path,
                    e5_dir,
                    name="K06-E05-retired-worker-unavailable",
                    selector={"edge": "K06-E05", "base_seed": base_seed},
                    root_dirs=[("r0", e5_r0, e5_b0)],
                    active_worker_count=0,
                    rename_root_idx=None,
                    swap_primary_idx=None,
                    check_retired_worker=True,
                    search_token="K06EdgeE05Token",
                )
            )

            all_results = generated_results + edge_results
            gen_passed = sum(1 for r in generated_results if r["verdict"] == "PASS")
            gen_failed = len(generated_results) - gen_passed
            edge_passed = sum(1 for r in edge_results if r["verdict"] == "PASS")
            edge_failed = len(edge_results) - edge_passed

            defect_counts: dict[str, int] = {}
            first_failure: dict[str, object] | None = None
            for r in all_results:
                for d in r["defects"]:  # type: ignore[union-attr]
                    code = str(d.get("code") or "unknown")
                    defect_counts[code] = defect_counts.get(code, 0) + 1
                if first_failure is None and r["verdict"] != "PASS":
                    first_failure = r

            report = {
                "family": "K06",
                "lane": "sidecar",
                "base_seed": base_seed,
                "generated_planned": instance_count,
                "generated_executed": len(generated_results),
                "generated_passed": gen_passed,
                "generated_failed": gen_failed,
                "edges_planned": 5,
                "edges_executed": len(edge_results),
                "edges_passed": edge_passed,
                "edges_failed": edge_failed,
                "defect_counts": defect_counts,
                "generated_results": generated_results,
                "edge_results": edge_results,
            }
            report_path = out / "k06_results.json"
            report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")

            artifacts: dict[str, Path] = {
                "launch_receipt": receipt_path,
                "k06_results": report_path,
            }
            if first_failure is not None:
                shrunk_path = out / "k06_shrunk_failure.json"
                shrunk_payload = {
                    "family": "K06",
                    "shrunk_minimal_repro": {
                        "name": first_failure["name"],
                        "selector": first_failure["selector"],
                        "defects": first_failure["defects"],
                        "retired_worker_result": first_failure.get("retired_worker_result"),
                        "worker_results": first_failure.get("worker_results"),
                    },
                }
                shrunk_path.write_text(json.dumps(shrunk_payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
                artifacts["k06_shrunk_failure"] = shrunk_path

            replay_cmd = [
                "python3",
                "-m",
                "catalog_feed_search.families.cf04_k06",
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
                "--instances",
                str(instance_count),
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
                    "family": "K06",
                    "generated_passed": gen_passed,
                    "generated_failed": gen_failed,
                    "edges_passed": edge_passed,
                    "edges_failed": edge_failed,
                    "defect_counts": defect_counts,
                },
                replay_command=replay_cmd,
                launch_receipt=launch_receipt,
            )
            return report
        finally:
            sidecar.stop()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run CF04 K06 (Roots, checkouts & worker branches)")
    parser.add_argument("--engine-bin", required=True)
    parser.add_argument("--d1-worktree", required=True)
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="")
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY)
    parser.add_argument("--instances", type=int, default=100)
    parser.add_argument("--rg-bin", default="rg")
    parser.add_argument("--repo-root", default="")
    args = parser.parse_args(argv)

    report = run_k06_suite(
        engine_bin=Path(args.engine_bin),
        d1_worktree=Path(args.d1_worktree),
        d1_commit=args.d1_commit,
        d1_tree=args.d1_tree,
        out_dir=Path(args.out_dir),
        base_seed=args.seed,
        instance_count=args.instances,
        rg_bin=args.rg_bin,
        repo_root=Path(args.repo_root) if args.repo_root else None,
    )
    print(
        json.dumps(
            {
                "family": "K06",
                "generated_passed": report["generated_passed"],
                "generated_failed": report["generated_failed"],
                "edges_passed": report["edges_passed"],
                "edges_failed": report["edges_failed"],
                "defect_counts": report["defect_counts"],
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
