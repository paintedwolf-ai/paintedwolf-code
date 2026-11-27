"""CF04 · F07: Root lifecycle — attach, detach, move, unmount and remount (40 sidecar + 4 edges)."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid

from ..driver_provenance import collect_run_provenance
from ..driver_sse import LiveSSESubscriber
from ..live_controls import wait_catalog_settled
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from .cf04_common import (
    CF04SidecarClient,
    count_linux_inotify_watches,
    count_workspace_live_watchers,
    finalize_family_evidence,
    sha256_bytes,
    start_sidecar,
    wait_root_watch_state,
)

F07_STEP_KINDS = ("attach", "write_observe", "move_away_and_back", "detach_and_verify_count")


@dataclass(frozen=True)
class F07ScriptSpec:
    """Generated root lifecycle script (1-6 steps)."""

    selector: ReplaySelector
    steps: tuple[str, ...]


def generate_f07_script(base_seed: str, instance_index: int) -> F07ScriptSpec:
    """Deterministically generate a 1-6 step F07 root lifecycle script."""
    sel = ReplaySelector(base_seed, "F07", instance_index)
    rng = seed_rng(base_seed, "F07", instance_index)
    step_count = rng.randint(1, 6)
    steps: list[str] = ["attach"]
    while len(steps) < step_count:
        steps.append(rng.choice(["write_observe", "move_away_and_back", "attach_second_then_detach"]))
    return F07ScriptSpec(selector=sel, steps=tuple(steps))


def _wait_sse_path(sub: LiveSSESubscriber, expected_path: str, max_wait_s: float = 8.0) -> tuple[bool, int]:
    """Wait until SSE stream reports expected_path or resync."""
    def _pred(frames: list[dict[str, object]]) -> bool:
        for fr in frames:
            if fr.get("topic") != "source_changed":
                continue
            data = fr.get("data")
            if not isinstance(data, dict):
                continue
            if data.get("resync") is True:
                return True
            for ch in data.get("changes") or []:
                if isinstance(ch, dict) and str(ch.get("path") or "").removeprefix("./") == expected_path:
                    return True
        return False

    frames, matched = sub.wait_for_frames(_pred, max_wait_s=max_wait_s)
    return matched, len(frames)


def _run_f07_lifecycle_instance(
    client: CF04SidecarClient,
    sidecar_pid: int,
    sidecar_url: str,
    sidecar_token: str,
    work_dir: Path,
    *,
    name: str,
    selector: dict[str, object],
    steps: tuple[str, ...],
) -> dict[str, object]:
    """Run one F07 lifecycle script against the live D1 sidecar."""
    defects: list[dict[str, object]] = []
    step_logs: list[dict[str, object]] = []

    root0 = work_dir / "root0"
    root0.mkdir(parents=True, exist_ok=True)
    initial_bytes = f"f07-{name}-initial\n".encode("utf-8")
    (root0 / "main.txt").write_bytes(initial_bytes)

    watches_before_attach = count_linux_inotify_watches(sidecar_pid)
    proj = client.create_project([{"path": str(root0.resolve()), "label": "root0", "is_primary": True}])
    project_id = str(proj["id"])
    root0_id = str(proj["roots"][0]["id"])  # type: ignore[index]

    # Verify watcher binds on open
    bound, ws_after_open = wait_root_watch_state(client, project_id, root0_id, {"live", "partial"}, max_wait_s=10.0)
    watches_after_attach = count_linux_inotify_watches(sidecar_pid)
    ws_summary_open = count_workspace_live_watchers(ws_after_open)
    if not bound:
        defects.append({"code": "watcher_did_not_bind_on_open", "workspace": ws_summary_open})

    step_logs.append(
        {
            "step": "open_and_bind",
            "bound": bound,
            "watch_summary": ws_summary_open,
            "linux_inotify_before": watches_before_attach,
            "linux_inotify_after": watches_after_attach,
        }
    )

    wait_catalog_settled(client.driver, project_id, root0_id, max_wait_s=10.0, poll_interval_s=0.05)

    for s_idx, step in enumerate(steps):
        if step == "attach":
            continue
        elif step == "write_observe":
            sub = LiveSSESubscriber(sidecar_url, sidecar_token, project_id=project_id, client_id=f"f07-{name}-{s_idx}")
            sub.start()
            ready = sub.wait_ready(max_wait_s=5.0)
            rel = f"edit_{s_idx}.txt"
            (root0 / rel).write_bytes(f"edit-{s_idx}\n".encode("utf-8"))
            observed, frame_count = _wait_sse_path(sub, rel, max_wait_s=8.0)
            sub.close()
            step_logs.append({"step": step, "sse_ready": ready, "observed": observed, "frame_count": frame_count})
            if not ready or not observed:
                defects.append({"code": "watcher_missed_write", "step_idx": s_idx, "path": rel, "sse_ready": ready})
        elif step == "move_away_and_back":
            moved_path = work_dir / f"root0-moved-{s_idx}"
            os.rename(root0, moved_path)
            try:
                # While moved away, reading the file or browsing MUST report an error/outage (never silent success)
                read_during = client.get_source_file(project_id, root0_id, "main.txt")
                browse_during = client.browse_dir(project_id, root0_id, ".")
                outage_reported = (read_during.status >= 400) and (browse_during.status >= 400)
                if not outage_reported:
                    defects.append(
                        {
                            "code": "move_outage_not_reported",
                            "read_status": read_during.status,
                            "browse_status": browse_during.status,
                        }
                    )
            finally:
                os.rename(moved_path, root0)

            # After moving back, root identity must remain identical and reads must recover
            read_after = client.get_source_file(project_id, root0_id, "main.txt")
            ws_after = client.get_source_workspace(project_id)
            recovered_id = ""
            if ws_after.status == 200 and isinstance(ws_after.json_body, dict):
                for r in ws_after.json_body.get("roots") or []:
                    if isinstance(r, dict) and str(r.get("id") or "") == root0_id:
                        recovered_id = str(r["id"])
            recovered = (
                read_after.status == 200
                and isinstance(read_after.json_body, dict)
                and str(read_after.json_body.get("sha256") or "") == sha256_bytes(initial_bytes)
                and recovered_id == root0_id
            )
            if not recovered:
                defects.append(
                    {
                        "code": "move_recovery_failed",
                        "read_status": read_after.status,
                        "recovered_id": recovered_id,
                        "expected_id": root0_id,
                    }
                )
            step_logs.append(
                {
                    "step": step,
                    "read_during_status": read_during.status,
                    "browse_during_status": browse_during.status,
                    "read_after_status": read_after.status,
                    "root_id_preserved": (recovered_id == root0_id),
                }
            )
        elif step == "attach_second_then_detach":
            root1 = work_dir / f"root1-{s_idx}"
            root1.mkdir(parents=True, exist_ok=True)
            (root1 / "sub").mkdir(exist_ok=True)
            (root1 / "sub" / "file.txt").write_bytes(b"second-root\n")

            w_before = count_linux_inotify_watches(sidecar_pid)
            att = client.attach_root(project_id, root1, label=f"root1-{s_idx}")
            if att.status not in (200, 201) or not isinstance(att.json_body, dict):
                defects.append({"code": "second_root_attach_failed", "status": att.status})
                continue
            root1_id = ""
            for r in att.json_body.get("roots") or []:
                if isinstance(r, dict) and str(Path(str(r.get("path") or "")).resolve()) == str(root1.resolve()):
                    root1_id = str(r["id"])
            bound1, ws1 = wait_root_watch_state(client, project_id, root1_id, {"live", "partial"}, max_wait_s=10.0)
            w_attached = count_linux_inotify_watches(sidecar_pid)
            sum_attached = count_workspace_live_watchers(ws1)

            det = client.detach_root(project_id, root1_id, force=True)
            # Wait briefly for watcher teardown
            deadline = time.monotonic() + 5.0
            waiter = threading.Event()
            w_detached = count_linux_inotify_watches(sidecar_pid)
            while w_attached is not None and w_detached is not None and w_detached >= w_attached and time.monotonic() < deadline:
                waiter.wait(0.1)
                w_detached = count_linux_inotify_watches(sidecar_pid)

            ws_after_det = client.get_source_workspace(project_id)
            sum_detached = (
                count_workspace_live_watchers(ws_after_det.json_body)
                if ws_after_det.status == 200 and isinstance(ws_after_det.json_body, dict)
                else {}
            )
            api_dropped = (
                det.status in (200, 204)
                and int(sum_detached.get("root_count", 99)) == int(sum_attached["root_count"]) - 1
                and root1_id not in (sum_detached.get("by_root") or {})
            )
            linux_dropped = True
            if w_attached is not None and w_detached is not None and w_before is not None and w_attached > w_before:
                linux_dropped = w_detached < w_attached

            if not bound1 or not api_dropped or not linux_dropped:
                defects.append(
                    {
                        "code": "watcher_teardown_on_detach_failed",
                        "bound1": bound1,
                        "detach_status": det.status,
                        "api_attached": sum_attached,
                        "api_detached": sum_detached,
                        "linux_watches_before": w_before,
                        "linux_watches_attached": w_attached,
                        "linux_watches_detached": w_detached,
                    }
                )
            step_logs.append(
                {
                    "step": step,
                    "bound1": bound1,
                    "detach_status": det.status,
                    "api_live_attached": sum_attached.get("live_or_partial_count"),
                    "api_live_detached": sum_detached.get("live_or_partial_count"),
                    "linux_watches_before": w_before,
                    "linux_watches_attached": w_attached,
                    "linux_watches_detached": w_detached,
                }
            )

    # Final project removal to release root0's watcher too
    client.delete_project_async(project_id, operation_id=str(uuid.uuid4()))

    return {
        "name": name,
        "selector": selector,
        "verdict": "PASS" if not defects else "DEFECT",
        "steps": list(steps),
        "step_logs": step_logs,
        "defects": defects,
    }


def _run_linux_real_mount_outage_edge(
    engine_bin: Path,
    d1_worktree: Path,
    harness_root: Path,
) -> dict[str, object]:
    """F07-E04 on Linux: isolated unshare -Urm user+mount namespace with real tmpfs mount/unmount/remount.

    Never touches host mounts; runs entirely inside an unprivileged user+mount namespace.
    """
    inner_script = r"""
import json, os, pathlib, subprocess, sys, tempfile, threading, time
sys.path.insert(0, sys.argv[1])
from catalog_feed_search.families.cf04_common import (
    CF04SidecarClient, count_linux_inotify_watches, count_workspace_live_watchers,
    sha256_bytes, start_sidecar, wait_root_watch_state,
)
from catalog_feed_search.driver_sse import LiveSSESubscriber
from catalog_feed_search.live_controls import wait_catalog_settled

engine_bin = pathlib.Path(sys.argv[2])
d1_worktree = pathlib.Path(sys.argv[3])
defects = []

with tempfile.TemporaryDirectory(prefix="cf04-f07-mnt-") as tmp_s:
    tmp = pathlib.Path(tmp_s)
    mnt = tmp / "vol"
    mnt.mkdir(parents=True, exist_ok=True)
    subprocess.run(["mount", "-t", "tmpfs", "-o", "size=16m", "tmpfs", str(mnt)], check=True)
    repo = mnt / "repo"
    repo.mkdir(parents=True, exist_ok=True)
    payload = b"cf04-linux-real-tmpfs-volume-bytes\n"
    (repo / "vol.txt").write_bytes(payload)

    sidecar = start_sidecar(engine_bin, tmp / "sidecar", cwd=d1_worktree)
    try:
        client = CF04SidecarClient(sidecar.url, sidecar.token)
        watches_before = count_linux_inotify_watches(sidecar.pid)
        proj = client.create_project([{"path": str(repo.resolve()), "label": "tmpfs-vol", "is_primary": True}])
        pid = str(proj["id"])
        rid = str(proj["roots"][0]["id"])

        bound, ws_before = wait_root_watch_state(client, pid, rid, {"live", "partial"}, max_wait_s=10.0)
        wait_catalog_settled(client.driver, pid, rid, max_wait_s=10.0, poll_interval_s=0.05)
        watches_live = count_linux_inotify_watches(sidecar.pid)
        read_before = client.get_source_file(pid, rid, "vol.txt")

        # Real unmount of the owned tmpfs volume!
        subprocess.run(["umount", "-l", str(mnt)], check=True)
        time.sleep(0.3)
        read_during = client.get_source_file(pid, rid, "vol.txt")
        browse_during = client.browse_dir(pid, rid, ".")
        ws_during = client.get_source_workspace(pid)
        outage_reported = (read_during.status >= 400) and (browse_during.status >= 400)
        if not outage_reported:
            defects.append({
                "code": "linux_unmount_outage_not_reported",
                "read_during": read_during.status,
                "browse_during": browse_during.status,
            })

        # Real remount of the owned tmpfs volume and restore repo!
        subprocess.run(["mount", "-t", "tmpfs", "-o", "size=16m", "tmpfs", str(mnt)], check=True)
        repo.mkdir(parents=True, exist_ok=True)
        (repo / "vol.txt").write_bytes(payload)
        time.sleep(0.3)

        read_after = client.get_source_file(pid, rid, "vol.txt")
        browse_after = client.browse_dir(pid, rid, ".")
        ws_after = client.get_source_workspace(pid)
        recovered = (
            read_after.status == 200
            and isinstance(read_after.json_body, dict)
            and str(read_after.json_body.get("sha256") or "") == sha256_bytes(payload)
            and browse_after.status == 200
        )
        if not recovered:
            defects.append({
                "code": "linux_remount_recovery_failed",
                "read_after": read_after.status,
                "browse_after": browse_after.status,
            })

        # Verify root identity preserved across unmount/remount
        after_ids = [
            str(r.get("id")) for r in (ws_after.json_body.get("roots") or [])
            if isinstance(r, dict)
        ] if isinstance(ws_after.json_body, dict) else []
        if after_ids != [rid]:
            defects.append({"code": "linux_remount_root_id_changed", "before": rid, "after": after_ids})

        # Detach and verify watcher teardown
        client.delete_project_async(pid, operation_id="f07-e04-cleanup")
        time.sleep(0.5)
        watches_after_cleanup = count_linux_inotify_watches(sidecar.pid)

        subprocess.run(["umount", "-l", str(mnt)], check=False)

        result = {
            "name": "F07-E04-linux-real-tmpfs-unmount-remount",
            "mechanism": "unshare -Urm isolated tmpfs mount/umount/remount",
            "verdict": "PASS" if not defects else "DEFECT",
            "bound_on_open": bound,
            "root_id": rid,
            "read_before_status": read_before.status,
            "read_during_unmount_status": read_during.status,
            "browse_during_unmount_status": browse_during.status,
            "read_after_remount_status": read_after.status,
            "browse_after_remount_status": browse_after.status,
            "workspace_before": count_workspace_live_watchers(ws_before),
            "workspace_during": count_workspace_live_watchers(ws_during.json_body) if isinstance(ws_during.json_body, dict) else {},
            "workspace_after": count_workspace_live_watchers(ws_after.json_body) if isinstance(ws_after.json_body, dict) else {},
            "linux_inotify_before": watches_before,
            "linux_inotify_live": watches_live,
            "linux_inotify_after_cleanup": watches_after_cleanup,
            "defects": defects,
        }
        print(json.dumps(result))
    finally:
        sidecar.stop()
"""
    pkg_parent = str(harness_root / "scripts" / "coordinator-benchmark")
    proc = subprocess.run(
        ["unshare", "-Urm", sys.executable, "-c", inner_script, pkg_parent, str(engine_bin), str(d1_worktree)],
        capture_output=True,
        text=True,
        check=False,
        timeout=90,
    )
    if proc.returncode != 0:
        return {
            "name": "F07-E04-linux-real-tmpfs-unmount-remount",
            "mechanism": "unshare -Urm isolated tmpfs mount/umount/remount",
            "verdict": "BLOCKED",
            "returncode": proc.returncode,
            "stdout": proc.stdout[-1000:],
            "stderr": proc.stderr[-1000:],
            "defects": [{"code": "linux_mount_fixture_failed", "stderr": proc.stderr[-500:]}],
        }
    for line in reversed(proc.stdout.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            return json.loads(line)
    return {
        "name": "F07-E04-linux-real-tmpfs-unmount-remount",
        "verdict": "BLOCKED",
        "stdout": proc.stdout[-1000:],
        "stderr": proc.stderr[-1000:],
        "defects": [{"code": "linux_mount_fixture_no_json"}],
    }


def _run_darwin_real_disk_image_outage_edge(
    engine_bin: Path,
    d1_worktree: Path,
) -> dict[str, object]:
    """F07-E04 on macOS: real APFS sparse disk image (hdiutil attach/detach/attach) against live D1 sidecar."""
    defects: list[dict[str, object]] = []
    with tempfile.TemporaryDirectory(prefix="cf04-f07-dmg-") as tmp_s:
        tmp = Path(tmp_s)
        dmg = tmp / "cf04vol.dmg"
        mnt = tmp / "vol"
        mnt.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["hdiutil", "create", "-size", "16m", "-fs", "APFS", "-volname", "CF04OutageVol", str(dmg)],
            check=True,
            capture_output=True,
        )
        subprocess.run(
            ["hdiutil", "attach", "-nobrowse", "-mountpoint", str(mnt), str(dmg)],
            check=True,
            capture_output=True,
        )
        try:
            repo = mnt / "repo"
            repo.mkdir(parents=True, exist_ok=True)
            payload = b"cf04-darwin-real-apfs-dmg-volume-bytes\n"
            (repo / "vol.txt").write_bytes(payload)

            sidecar = start_sidecar(engine_bin, tmp / "sidecar", cwd=d1_worktree)
            try:
                client = CF04SidecarClient(sidecar.url, sidecar.token)
                proj = client.create_project([{"path": str(repo.resolve()), "label": "apfs-vol", "is_primary": True}])
                pid = str(proj["id"])
                rid = str(proj["roots"][0]["id"])  # type: ignore[index]

                bound, ws_before = wait_root_watch_state(client, pid, rid, {"live", "partial"}, max_wait_s=10.0)
                wait_catalog_settled(client.driver, pid, rid, max_wait_s=10.0, poll_interval_s=0.05)
                read_before = client.get_source_file(pid, rid, "vol.txt")

                # Real unmount of the disk image volume!
                subprocess.run(["hdiutil", "detach", "-force", str(mnt)], check=True, capture_output=True)
                time.sleep(0.5)
                read_during = client.get_source_file(pid, rid, "vol.txt")
                browse_during = client.browse_dir(pid, rid, ".")
                ws_during = client.get_source_workspace(pid)
                if not (read_during.status >= 400 and browse_during.status >= 400):
                    defects.append(
                        {
                            "code": "darwin_unmount_outage_not_reported",
                            "read_during": read_during.status,
                            "browse_during": browse_during.status,
                        }
                    )

                # Real remount of the disk image volume at the exact same mountpoint!
                mnt.mkdir(parents=True, exist_ok=True)
                subprocess.run(
                    ["hdiutil", "attach", "-nobrowse", "-mountpoint", str(mnt), str(dmg)],
                    check=True,
                    capture_output=True,
                )
                time.sleep(0.5)
                read_after = client.get_source_file(pid, rid, "vol.txt")
                browse_after = client.browse_dir(pid, rid, ".")
                ws_after = client.get_source_workspace(pid)
                recovered = (
                    read_after.status == 200
                    and isinstance(read_after.json_body, dict)
                    and str(read_after.json_body.get("sha256") or "") == sha256_bytes(payload)
                    and browse_after.status == 200
                )
                if not recovered:
                    defects.append(
                        {
                            "code": "darwin_remount_recovery_failed",
                            "read_after": read_after.status,
                            "browse_after": browse_after.status,
                        }
                    )

                client.delete_project_async(pid, operation_id="f07-e04-darwin-cleanup")
                return {
                    "name": "F07-E04-darwin-real-apfs-unmount-remount",
                    "mechanism": "hdiutil APFS disk image attach/detach/attach",
                    "verdict": "PASS" if not defects else "DEFECT",
                    "bound_on_open": bound,
                    "root_id": rid,
                    "read_before_status": read_before.status,
                    "read_during_unmount_status": read_during.status,
                    "browse_during_unmount_status": browse_during.status,
                    "read_after_remount_status": read_after.status,
                    "browse_after_remount_status": browse_after.status,
                    "workspace_before": count_workspace_live_watchers(ws_before),
                    "workspace_during": count_workspace_live_watchers(ws_during.json_body) if isinstance(ws_during.json_body, dict) else {},
                    "workspace_after": count_workspace_live_watchers(ws_after.json_body) if isinstance(ws_after.json_body, dict) else {},
                    "defects": defects,
                }
            finally:
                sidecar.stop()
        finally:
            subprocess.run(["hdiutil", "detach", "-force", str(mnt)], check=False, capture_output=True)


def run_f07_suite(
    *,
    engine_bin: Path,
    d1_worktree: Path,
    d1_commit: str,
    d1_tree: str,
    out_dir: Path,
    base_seed: str = BASE_SEED_PRIMARY,
    instance_count: int = 40,
    rg_bin: str = "rg",
    repo_root: Path | None = None,
) -> dict[str, object]:
    """Run all F07 generated sidecar instances (default 40) + 4 hand-written edge slots."""
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    harness_root = Path(repo_root).resolve() if repo_root else Path(__file__).resolve().parents[4]

    with tempfile.TemporaryDirectory(prefix="cf04-f07-") as tmp_str:
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
                spec = generate_f07_script(base_seed, idx)
                inst_dir = tmp / f"inst-{idx:03d}"
                inst_dir.mkdir(parents=True, exist_ok=True)
                res = _run_f07_lifecycle_instance(
                    client,
                    sidecar.pid,
                    sidecar.url,
                    sidecar.token,
                    inst_dir,
                    name=f"F07-inst-{idx:03d}",
                    selector=spec.selector.to_dict(),
                    steps=spec.steps,
                )
                generated_results.append(res)
                shutil.rmtree(inst_dir, ignore_errors=True)

            # 4 Hand-written edges
            edge_results: list[dict[str, object]] = []
            for e_name, e_steps in (
                ("F07-E01-attach-bind-observe", ("attach", "write_observe")),
                ("F07-E02-move-outage-recovery", ("attach", "move_away_and_back")),
                ("F07-E03-detach-watcher-teardown-count", ("attach", "attach_second_then_detach")),
            ):
                edir = tmp / e_name
                edir.mkdir(parents=True, exist_ok=True)
                edge_results.append(
                    _run_f07_lifecycle_instance(
                        client,
                        sidecar.pid,
                        sidecar.url,
                        sidecar.token,
                        edir,
                        name=e_name,
                        selector={"edge": e_name, "base_seed": base_seed},
                        steps=e_steps,
                    )
                )
                shutil.rmtree(edir, ignore_errors=True)
        finally:
            sidecar.stop()

        # F07-E04: Real volume unmount/remount outage (isolated tmpfs on Linux, APFS dmg on macOS)
        if platform.system().lower() == "linux":
            e4 = _run_linux_real_mount_outage_edge(engine_bin, d1_worktree, harness_root)
        elif platform.system().lower() == "darwin":
            e4 = _run_darwin_real_disk_image_outage_edge(engine_bin, d1_worktree)
        else:
            e4 = {"name": "F07-E04-unsupported-os", "verdict": "BLOCKED", "defects": [{"code": "unsupported_os"}]}
        edge_results.append(e4)

        gen_passed = sum(1 for r in generated_results if r["verdict"] == "PASS")
        gen_failed = len(generated_results) - gen_passed
        edge_passed = sum(1 for r in edge_results if r["verdict"] == "PASS")
        edge_failed = len(edge_results) - edge_passed

        defect_counts: dict[str, int] = {}
        first_failure: dict[str, object] | None = None
        for r in generated_results + edge_results:
            for d in r.get("defects") or []:  # type: ignore[union-attr]
                code = str(d.get("code") or "unknown")
                defect_counts[code] = defect_counts.get(code, 0) + 1
            if first_failure is None and r["verdict"] != "PASS":
                first_failure = r

        report = {
            "family": "F07",
            "lane": "sidecar",
            "base_seed": base_seed,
            "generated_planned": instance_count,
            "generated_executed": len(generated_results),
            "generated_passed": gen_passed,
            "generated_failed": gen_failed,
            "edges_planned": 4,
            "edges_executed": len(edge_results),
            "edges_passed": edge_passed,
            "edges_failed": edge_failed,
            "defect_counts": defect_counts,
            "generated_results": generated_results,
            "edge_results": edge_results,
        }
        report_path = out / "f07_results.json"
        report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")

        artifacts: dict[str, Path] = {
            "launch_receipt": receipt_path,
            "f07_results": report_path,
        }
        if first_failure is not None:
            shrunk_path = out / "f07_shrunk_failure.json"
            shrunk_path.write_text(json.dumps(first_failure, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            artifacts["f07_shrunk_failure"] = shrunk_path

        replay_cmd = [
            "python3",
            "-m",
            "catalog_feed_search.families.cf04_f07",
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
                "family": "F07",
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


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run CF04 F07 (Root lifecycle)")
    parser.add_argument("--engine-bin", required=True)
    parser.add_argument("--d1-worktree", required=True)
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="")
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY)
    parser.add_argument("--instances", type=int, default=40)
    parser.add_argument("--rg-bin", default="rg")
    parser.add_argument("--repo-root", default="")
    args = parser.parse_args(argv)

    report = run_f07_suite(
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
                "family": "F07",
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
