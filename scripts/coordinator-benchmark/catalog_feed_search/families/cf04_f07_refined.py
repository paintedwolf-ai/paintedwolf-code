"""CF04 · F07 Refined Diagnostic Replay (Decision 026): inode-exact inotify teardown, mountinfo-verified unmount, and SSE drain proof."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tempfile
import time
import uuid

from ..driver_provenance import collect_run_provenance
from ..driver_sse import LiveSSESubscriber
from ..live_controls import wait_catalog_settled
from .cf04_common import (
    CF04SidecarClient,
    count_workspace_live_watchers,
    finalize_family_evidence,
    sha256_bytes,
    start_sidecar,
    wait_root_watch_state,
)

_INOTIFY_RE = re.compile(r"^inotify\s+wd:([0-9a-fA-F]+)\s+ino:([0-9a-fA-F]+)\s+sdev:([0-9a-fA-F]+)")


def read_linux_inotify_entries(pid: int) -> list[dict[str, object]]:
    """Parse exact kernel inotify watch entries (wd, ino, sdev) from /proc/<pid>/fdinfo/*."""
    if platform.system().lower() != "linux":
        return []
    fdinfo_dir = Path(f"/proc/{pid}/fdinfo")
    if not fdinfo_dir.is_dir():
        return []
    entries: list[dict[str, object]] = []
    for entry in sorted(fdinfo_dir.iterdir(), key=lambda p: p.name):
        try:
            text = entry.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for line in text.splitlines():
            m = _INOTIFY_RE.match(line.strip())
            if m:
                entries.append(
                    {
                        "fd": entry.name,
                        "wd": int(m.group(1), 16),
                        "ino": int(m.group(2), 16),
                        "ino_hex": m.group(2).lower(),
                        "sdev_hex": m.group(3).lower(),
                    }
                )
    return entries


def _wait_sse_path(sub: LiveSSESubscriber, expected_path: str, max_wait_s: float = 10.0) -> tuple[bool, list[dict[str, object]]]:
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
    return matched, frames


def run_linux_inode_detach_check(engine_bin: Path, d1_worktree: Path) -> dict[str, object]:
    """Decision 026 §1: Isolate 2 roots, correlate exact stat(root).st_ino against /proc/<pid>/fdinfo/* before & after detach."""
    with tempfile.TemporaryDirectory(prefix="cf04-f07-ino-") as tmp_s:
        tmp = Path(tmp_s)
        root0 = tmp / "surviving-root"
        root1 = tmp / "detached-root"
        root0.mkdir(parents=True, exist_ok=True)
        root1.mkdir(parents=True, exist_ok=True)
        (root1 / "sub").mkdir(exist_ok=True)
        (root0 / "keep.txt").write_bytes(b"surviving\n")
        (root1 / "sub" / "drop.txt").write_bytes(b"detached\n")

        ino_root0 = os.stat(root0).st_ino
        ino_root1 = os.stat(root1).st_ino
        ino_root1_sub = os.stat(root1 / "sub").st_ino

        sidecar = start_sidecar(engine_bin, tmp / "sidecar", cwd=d1_worktree)
        try:
            client = CF04SidecarClient(sidecar.url, sidecar.token)
            baseline_entries = read_linux_inotify_entries(sidecar.pid)

            proj = client.create_project([{"path": str(root0.resolve()), "label": "surviving", "is_primary": True}])
            pid = str(proj["id"])
            rid0 = str(proj["roots"][0]["id"])  # type: ignore[index]
            wait_root_watch_state(client, pid, rid0, {"live", "partial"}, max_wait_s=10.0)
            wait_catalog_settled(client.driver, pid, rid0, max_wait_s=10.0, poll_interval_s=0.05)

            att = client.attach_root(pid, root1, label="detached")
            rid1 = ""
            if isinstance(att.json_body, dict):
                for r in att.json_body.get("roots") or []:
                    if isinstance(r, dict) and str(Path(str(r.get("path") or "")).resolve()) == str(root1.resolve()):
                        rid1 = str(r["id"])
            wait_root_watch_state(client, pid, rid1, {"live", "partial"}, max_wait_s=10.0)
            wait_catalog_settled(client.driver, pid, rid1, max_wait_s=10.0, poll_interval_s=0.05)
            time.sleep(0.5)

            attached_entries = read_linux_inotify_entries(sidecar.pid)
            attached_inos = {int(e["ino"]) for e in attached_entries}

            det = client.detach_root(pid, rid1, force=True)
            # Sample across a 6-second drain boundary
            samples_after_detach: list[dict[str, object]] = []
            for sec in (0.2, 1.0, 3.0, 6.0):
                time.sleep(sec if not samples_after_detach else 2.0)
                cur = read_linux_inotify_entries(sidecar.pid)
                cur_inos = {int(e["ino"]) for e in cur}
                samples_after_detach.append(
                    {
                        "elapsed_s": sec,
                        "total_wd_count": len(cur),
                        "surviving_root_watched": ino_root0 in cur_inos,
                        "detached_root_watched": ino_root1 in cur_inos,
                        "detached_sub_watched": ino_root1_sub in cur_inos,
                    }
                )

            final_entries = read_linux_inotify_entries(sidecar.pid)
            final_inos = {int(e["ino"]) for e in final_entries}
            detached_leaked = (ino_root1 in final_inos) or (ino_root1_sub in final_inos)
            surviving_kept = ino_root0 in final_inos

            return {
                "check": "F07-1-inode-exact-detach",
                "verdict": "DEFECT" if detached_leaked or not surviving_kept else "PASS",
                "detach_status": det.status,
                "inodes": {
                    "surviving_root": ino_root0,
                    "detached_root": ino_root1,
                    "detached_sub": ino_root1_sub,
                },
                "baseline_wd_count": len(baseline_entries),
                "attached_wd_count": len(attached_entries),
                "attached_has_surviving": ino_root0 in attached_inos,
                "attached_has_detached": ino_root1 in attached_inos,
                "attached_has_detached_sub": ino_root1_sub in attached_inos,
                "samples_after_detach": samples_after_detach,
                "final_wd_count": len(final_entries),
                "detached_root_leaked": detached_leaked,
                "surviving_root_kept": surviving_kept,
            }
        finally:
            sidecar.stop()


def run_linux_mountinfo_unmount_check(engine_bin: Path, d1_worktree: Path, harness_root: Path) -> dict[str, object]:
    """Decision 026 §2: Verify /proc/<sidecar_pid>/mountinfo sees real unmount, poll watch.state for 5s, and prove post-remount SSE write."""
    inner = r"""
import json, os, pathlib, subprocess, sys, tempfile, time
sys.path.insert(0, sys.argv[1])
from catalog_feed_search.families.cf04_common import (
    CF04SidecarClient, count_workspace_live_watchers, sha256_bytes, start_sidecar, wait_root_watch_state,
)
from catalog_feed_search.driver_sse import LiveSSESubscriber
from catalog_feed_search.live_controls import wait_catalog_settled

def mountinfo_has(pid, target_str):
    txt = pathlib.Path(f"/proc/{pid}/mountinfo").read_text(encoding="utf-8", errors="replace")
    return [line for line in txt.splitlines() if target_str in line]

engine_bin = pathlib.Path(sys.argv[2])
d1_worktree = pathlib.Path(sys.argv[3])

with tempfile.TemporaryDirectory(prefix="cf04-f07-mi-") as tmp_s:
    tmp = pathlib.Path(tmp_s)
    mnt = tmp / "vol"
    mnt.mkdir(parents=True, exist_ok=True)
    subprocess.run(["mount", "-t", "tmpfs", "-o", "size=16m", "tmpfs", str(mnt)], check=True)
    repo = mnt / "repo"
    repo.mkdir(parents=True, exist_ok=True)
    payload = b"cf04-f07-refined-mountinfo-bytes\n"
    (repo / "vol.txt").write_bytes(payload)

    sidecar = start_sidecar(engine_bin, tmp / "sidecar", cwd=d1_worktree)
    try:
        client = CF04SidecarClient(sidecar.url, sidecar.token)
        mi_before = mountinfo_has(sidecar.pid, str(mnt))
        proj = client.create_project([{"path": str(repo.resolve()), "label": "vol", "is_primary": True}])
        pid = str(proj["id"])
        rid = str(proj["roots"][0]["id"])
        bound, ws_before = wait_root_watch_state(client, pid, rid, {"live", "partial"}, max_wait_s=10.0)
        wait_catalog_settled(client.driver, pid, rid, max_wait_s=10.0, poll_interval_s=0.05)

        # Unmount the tmpfs volume in the shared namespace
        subprocess.run(["umount", "-l", str(mnt)], check=True)
        mi_during = mountinfo_has(sidecar.pid, str(mnt))

        # Poll watch.state across a 5-second observation boundary during unmount
        during_samples = []
        for idx in range(6):
            ws = client.get_source_workspace(pid)
            summary = count_workspace_live_watchers(ws.json_body) if isinstance(ws.json_body, dict) else {}
            during_samples.append({
                "poll_idx": idx,
                "watch_state": (summary.get("by_root") or {}).get(rid),
            })
            if idx < 5:
                time.sleep(1.0)
        read_during = client.get_source_file(pid, rid, "vol.txt")
        browse_during = client.browse_dir(pid, rid, ".")

        # Remount and verify post-remount read + live SSE write observation!
        subprocess.run(["mount", "-t", "tmpfs", "-o", "size=16m", "tmpfs", str(mnt)], check=True)
        mi_after = mountinfo_has(sidecar.pid, str(mnt))
        repo.mkdir(parents=True, exist_ok=True)
        (repo / "vol.txt").write_bytes(payload)
        time.sleep(0.5)

        read_after = client.get_source_file(pid, rid, "vol.txt")
        sub = LiveSSESubscriber(sidecar.url, sidecar.token, project_id=pid, client_id="f07-refined-remount")
        sub.start()
        sse_ready = sub.wait_ready(max_wait_s=5.0)
        (repo / "post_remount.txt").write_bytes(b"post-remount-write\n")
        def _pred(frames):
            for fr in frames:
                if fr.get("topic") == "source_changed" and isinstance(fr.get("data"), dict):
                    if fr["data"].get("resync") is True:
                        return True
                    for ch in fr["data"].get("changes") or []:
                        if isinstance(ch, dict) and str(ch.get("path") or "").removeprefix("./") == "post_remount.txt":
                            return True
            return False
        _, post_remount_sse_observed = sub.wait_for_frames(_pred, max_wait_s=8.0)
        sub.close()

        subprocess.run(["umount", "-l", str(mnt)], check=False)

        remained_live_all_5s = all(s["watch_state"] == "live" for s in during_samples)
        print(json.dumps({
            "check": "F07-2-linux-mountinfo-unmount-remount",
            "verdict": "DEFECT" if (remained_live_all_5s or not post_remount_sse_observed) else "PASS",
            "sidecar_mountinfo_present_before": len(mi_before) > 0,
            "sidecar_mountinfo_present_during": len(mi_during) > 0,
            "sidecar_mountinfo_present_after": len(mi_after) > 0,
            "bound_on_open": bound,
            "read_during_status": read_during.status,
            "browse_during_status": browse_during.status,
            "during_watch_state_5s_samples": during_samples,
            "remained_live_all_5s": remained_live_all_5s,
            "read_after_status": read_after.status,
            "post_remount_sse_ready": sse_ready,
            "post_remount_sse_observed": post_remount_sse_observed,
        }))
    finally:
        sidecar.stop()
"""
    pkg_parent = str(harness_root / "scripts" / "coordinator-benchmark")
    proc = subprocess.run(
        ["unshare", "-Urm", sys.executable, "-c", inner, pkg_parent, str(engine_bin), str(d1_worktree)],
        capture_output=True,
        text=True,
        check=False,
        timeout=90,
    )
    for line in reversed(proc.stdout.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            return json.loads(line)
    return {"check": "F07-2-linux-mountinfo-unmount-remount", "verdict": "BLOCKED", "stderr": proc.stderr[-800:]}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run CF04 F07 Decision-026 refined diagnostics")
    parser.add_argument("--engine-bin", required=True)
    parser.add_argument("--d1-worktree", required=True)
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="")
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--rg-bin", default="rg")
    parser.add_argument("--repo-root", default="")
    args = parser.parse_args(argv)

    out = Path(args.out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    harness_root = Path(args.repo_root).resolve() if args.repo_root else Path(__file__).resolve().parents[4]

    results: dict[str, object] = {}
    if platform.system().lower() == "linux":
        results["f07_1_inode_detach"] = run_linux_inode_detach_check(Path(args.engine_bin), Path(args.d1_worktree))
        results["f07_2_mountinfo_unmount"] = run_linux_mountinfo_unmount_check(
            Path(args.engine_bin), Path(args.d1_worktree), harness_root
        )

    res_path = out / "f07_refined_results.json"
    res_path.write_text(json.dumps(results, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    with tempfile.TemporaryDirectory() as tmp_s:
        prov = collect_run_provenance(
            harness_repo=harness_root,
            work_dir=Path(tmp_s),
            binary_path=Path(args.engine_bin),
            d1_commit=args.d1_commit,
            d1_tree=args.d1_tree,
            rg_bin=args.rg_bin,
        )
    finalize_family_evidence(
        out,
        prov,
        {"f07_refined_results": res_path},
        {"family": "F07-refined", "checks": list(results.keys())},
        replay_command=["python3", "-m", "catalog_feed_search.families.cf04_f07_refined", "--out-dir", "."],
    )
    print(json.dumps(results, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
