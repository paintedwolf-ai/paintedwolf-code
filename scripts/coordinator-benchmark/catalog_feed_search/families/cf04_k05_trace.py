"""CF04 · K05 OS-level strace operation-trace observer & small-fixture controls (Decision 053, D6-compliant)."""

from __future__ import annotations

import argparse
import contextlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

from ..driver_provenance import collect_run_provenance
from ..live_controls import wait_catalog_settled
from .cf04_common import CF04SidecarClient, finalize_family_evidence, free_tcp_port
from .cf04_k05 import write_scalefixture_tree

# Examples with `strace -f -y`:
# 12345 getdents64(14</tmp/fix/g000/d000>, 0x..., 8192) = 1600
# 12345 newfstatat(14</tmp/fix/g000/d000>, "f000.go", {...}, AT_SYMLINK_NOFOLLOW) = 0
# 12345 newfstatat(AT_FDCWD, "/tmp/fix/g000/d000/f000.go", {...}, 0) = 0
# 12345 statx(14</tmp/fix/g000/d000>, "f000.go", ...) = 0
_GETDENTS_RE = re.compile(r"getdents64\(\d+<([^>]+)>,.*=\s*(-?\d+)")
_FSTATAT_FD_RE = re.compile(r"(?:newfstatat|fstatat64|statx)\(\d+<([^>]+)>,\s*\"([^\"]*)\"")
_FSTATAT_CWD_RE = re.compile(r"(?:newfstatat|fstatat64|statx)\(AT_FDCWD,\s*\"([^\"]+)\"")


def _under_root(path_str: str, root_str: str) -> str | None:
    """Return normalized relative path if path_str is at or under root_str, else None."""
    norm = os.path.normpath(path_str)
    if norm == root_str:
        return "."
    prefix = root_str.rstrip("/") + "/"
    if norm.startswith(prefix):
        return norm[len(prefix) :]
    return None


def parse_strace_fixture_ops(
    strace_text: str,
    fixture_root: Path,
    *,
    known_dir_rels: set[str] | None = None,
) -> dict[str, object]:
    """Parse `strace -f -y` output scoped strictly to fixture_root.

    Returns `status="unknown"` if the trace text is empty or contains zero syscall lines,
    preventing missing instrumentation from masquerading as zero reads.
    """
    lines = [ln for ln in strace_text.splitlines() if ln.strip()]
    if not lines:
        return {
            "status": "unknown",
            "reason": "empty strace output",
            "directory_read_calls": "unknown",
            "distinct_directories_read": "unknown",
            "per_file_metadata_reads": "unknown",
            "directory_metadata_reads": "unknown",
        }

    root_path = Path(fixture_root)
    root_str = str(root_path.resolve()) if root_path.exists() else os.path.normpath(str(root_path))
    dirs_known = set(known_dir_rels) if known_dir_rels is not None else set()
    dirs_known.add(".")

    dir_read_calls = 0
    distinct_dirs: set[str] = set()
    file_stat_calls = 0
    distinct_file_stats: set[str] = set()
    dir_stat_calls = 0

    for line in lines:
        mg = _GETDENTS_RE.search(line)
        if mg:
            dir_path, ret_str = mg.group(1), mg.group(2)
            rel = _under_root(dir_path, root_str)
            if rel is not None and int(ret_str) > 0:
                dir_read_calls += 1
                distinct_dirs.add(rel)
            continue

        mf = _FSTATAT_FD_RE.search(line)
        if mf:
            base_dir, name = mf.group(1), mf.group(2)
            full = base_dir if name in ("", ".") else os.path.join(base_dir, name)
            rel = _under_root(full, root_str)
            if rel is not None:
                if rel in dirs_known:
                    dir_stat_calls += 1
                else:
                    file_stat_calls += 1
                    distinct_file_stats.add(rel)
            continue

        mc = _FSTATAT_CWD_RE.search(line)
        if mc:
            rel = _under_root(mc.group(1), root_str)
            if rel is not None:
                if rel in dirs_known:
                    dir_stat_calls += 1
                else:
                    file_stat_calls += 1
                    distinct_file_stats.add(rel)

    return {
        "status": "ok",
        "trace_lines": len(lines),
        "directory_read_calls": dir_read_calls,
        "distinct_directories_read": len(distinct_dirs),
        "per_file_metadata_reads": file_stat_calls,
        "distinct_files_statted": len(distinct_file_stats),
        "directory_metadata_reads": dir_stat_calls,
    }


def _collect_dir_rels(root: Path) -> set[str]:
    rels: set[str] = {"."}
    for p in root.rglob("*"):
        if p.is_dir():
            rels.add(p.relative_to(root).as_posix())
    return rels


def run_small_fixture_strace_controls(
    *,
    engine_bin: Path,
    d1_worktree: Path,
    d1_commit: str,
    d1_tree: str,
    out_dir: Path,
    rg_bin: str = "rg",
    repo_root: Path | None = None,
) -> dict[str, object]:
    """Run Decision-053 positive/negative controls + real D1 sidecar small-fixture strace proof on Linux."""
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    harness_root = Path(repo_root).resolve() if repo_root else Path(__file__).resolve().parents[4]

    if platform.system().lower() != "linux":
        res = {
            "platform": platform.system(),
            "status": "unsupported_unprivileged",
            "reason": "macOS fs_usage requires root; unprivileged operation tracing is unavailable without altering host/SIP settings",
        }
        res_path = out / "k05_trace_controls.json"
        res_path.write_text(json.dumps(res, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return res

    with tempfile.TemporaryDirectory(prefix="cf04-k05-trace-") as tmp_s:
        tmp = Path(tmp_s)
        # 1. Construct small synthetic scalefixture AFTER which strace starts (20 files, 3 dirs: ., g000, g000/d000)
        ctrl_tree = tmp / "ctrl-tree"
        ctrl_shape = write_scalefixture_tree(ctrl_tree, 20)
        ctrl_dirs = _collect_dir_rels(ctrl_tree)

        # Control A: scandir-only (directory reads, 0 per-file stats)
        trace_a = tmp / "ctrl_a.strace"
        scandir_code = (
            "import os, sys\n"
            "def walk(d):\n"
            "  with os.scandir(d) as it:\n"
            "    for e in it:\n"
            "      if e.is_dir(follow_symlinks=False): walk(e.path)\n"
            "walk(sys.argv[1])\n"
        )
        subprocess.run(
            ["strace", "-f", "-y", "-e", "trace=getdents64,newfstatat,statx", "-o", str(trace_a), sys.executable, "-c", scandir_code, str(ctrl_tree)],
            check=True,
            capture_output=True,
        )
        obs_a = parse_strace_fixture_ops(trace_a.read_text(encoding="utf-8", errors="replace"), ctrl_tree, known_dir_rels=ctrl_dirs)
        ctrl_a_pass = (
            obs_a["status"] == "ok"
            and obs_a["distinct_directories_read"] == ctrl_shape.total_directories
            and obs_a["per_file_metadata_reads"] == 0
        )

        # Control B: scandir + lstat on every file (directory reads + 20 per-file stats)
        trace_b = tmp / "ctrl_b.strace"
        stat_code = (
            "import os, sys\n"
            "def walk(d):\n"
            "  with os.scandir(d) as it:\n"
            "    for e in it:\n"
            "      if e.is_dir(follow_symlinks=False): walk(e.path)\n"
            "      else: os.lstat(e.path)\n"
            "walk(sys.argv[1])\n"
        )
        subprocess.run(
            ["strace", "-f", "-y", "-e", "trace=getdents64,newfstatat,statx", "-o", str(trace_b), sys.executable, "-c", stat_code, str(ctrl_tree)],
            check=True,
            capture_output=True,
        )
        obs_b = parse_strace_fixture_ops(trace_b.read_text(encoding="utf-8", errors="replace"), ctrl_tree, known_dir_rels=ctrl_dirs)
        ctrl_b_pass = (
            obs_b["status"] == "ok"
            and obs_b["distinct_directories_read"] == ctrl_shape.total_directories
            and obs_b["per_file_metadata_reads"] == 20
        )

        # Control C: negative control (empty/missing trace -> unknown)
        obs_c = parse_strace_fixture_ops("", ctrl_tree, known_dir_rels=ctrl_dirs)
        ctrl_c_pass = obs_c["status"] == "unknown"

        # 2. Real D1 sidecar under strace on a 50-file scalefixture (fixture built BEFORE strace starts!)
        sidecar_tree = tmp / "sidecar-tree"
        sidecar_shape = write_scalefixture_tree(sidecar_tree, 50)
        sidecar_dirs = _collect_dir_rels(sidecar_tree)
        trace_d1 = tmp / "d1_sidecar.strace"

        config_dir = tmp / "sidecar-cfg"
        config_dir.mkdir(parents=True, exist_ok=True)
        db_path = config_dir / "store.db"
        port = free_tcp_port()
        token = secrets.token_hex(16)
        addr = f"127.0.0.1:{port}"
        url = f"http://{addr}"
        env = dict(os.environ)
        env.update(
            {
                "LYCAON_ADDR": addr,
                "LYCAON_API_TOKEN": token,
                "LYCAON_CONFIG_DIR": str(config_dir),
                "LYCAON_LLM_MOCK": "1",
                "LYCAON_LOG_LEVEL": "info",
            }
        )
        stderr_log = tmp / "d1_stderr.log"
        stderr_fh = stderr_log.open("wb")
        proc = subprocess.Popen(
            [
                "strace",
                "-f",
                "-y",
                "-e",
                "trace=getdents64,newfstatat,statx",
                "-o",
                str(trace_d1),
                str(engine_bin),
                "serve",
                "--db",
                str(db_path),
            ],
            cwd=str(d1_worktree),
            env=env,
            stdout=stderr_fh,
            stderr=stderr_fh,
        )
        stderr_fh.close()
        settled_file_count = -1
        try:
            deadline = time.monotonic() + 25.0
            waiter = threading.Event()
            healthy = False
            while time.monotonic() < deadline:
                try:
                    with urllib.request.urlopen(f"{url}/health", timeout=1.0) as resp:
                        if resp.status == 200:
                            healthy = True
                            break
                except Exception:
                    pass
                waiter.wait(0.1)
            if not healthy:
                raise RuntimeError("straced D1 sidecar did not become healthy")

            client = CF04SidecarClient(url, token)
            proj = client.create_project([{"path": str(sidecar_tree.resolve()), "label": "k05-trace", "is_primary": True}])
            pid = str(proj["id"])
            rid = str(proj["roots"][0]["id"])  # type: ignore[index]
            settled, _, summary = wait_catalog_settled(client.driver, pid, rid, max_wait_s=20.0, poll_interval_s=0.1)
            settled_file_count = int(summary.get("file_count", -1))
        finally:
            with contextlib.suppress(OSError):
                proc.send_signal(signal.SIGINT)
            try:
                proc.wait(timeout=10.0)
            except subprocess.TimeoutExpired:
                proc.terminate()
                proc.wait(timeout=3.0)

        obs_d1 = parse_strace_fixture_ops(
            trace_d1.read_text(encoding="utf-8", errors="replace") if trace_d1.is_file() else "",
            sidecar_tree,
            known_dir_rels=sidecar_dirs,
        )

        report = {
            "platform": platform.system(),
            "observer": "/usr/bin/strace -f -y -e trace=getdents64,newfstatat,statx",
            "controls": {
                "control_a_dir_reads_only": {"passed": ctrl_a_pass, "expected_dirs": ctrl_shape.total_directories, "expected_file_stats": 0, "observed": obs_a},
                "control_b_dir_plus_file_stats": {"passed": ctrl_b_pass, "expected_dirs": ctrl_shape.total_directories, "expected_file_stats": 20, "observed": obs_b},
                "control_c_empty_trace_unknown": {"passed": ctrl_c_pass, "observed": obs_c},
                "all_controls_passed": ctrl_a_pass and ctrl_b_pass and ctrl_c_pass,
            },
            "d1_small_fixture_trace": {
                "files_target": sidecar_shape.files,
                "expected_total_dirs": sidecar_shape.total_directories,
                "settled_file_count": settled_file_count,
                "observed": obs_d1,
            },
        }
        res_path = out / "k05_trace_controls.json"
        res_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")

        prov = collect_run_provenance(
            harness_repo=harness_root,
            work_dir=tmp,
            binary_path=engine_bin,
            d1_commit=d1_commit,
            d1_tree=d1_tree,
            rg_bin=rg_bin,
        )
        finalize_family_evidence(
            out,
            prov,
            {"k05_trace_controls": res_path},
            {
                "family": "K05-trace-controls",
                "all_controls_passed": ctrl_a_pass and ctrl_b_pass and ctrl_c_pass,
                "d1_observed": obs_d1,
            },
            replay_command=["python3", "-m", "catalog_feed_search.families.cf04_k05_trace", "--out-dir", "."],
        )
        return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run Decision-053 K05 strace operation-trace controls")
    parser.add_argument("--engine-bin", required=True)
    parser.add_argument("--d1-worktree", required=True)
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="")
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--rg-bin", default="rg")
    parser.add_argument("--repo-root", default="")
    args = parser.parse_args(argv)

    rep = run_small_fixture_strace_controls(
        engine_bin=Path(args.engine_bin),
        d1_worktree=Path(args.d1_worktree),
        d1_commit=args.d1_commit,
        d1_tree=args.d1_tree,
        out_dir=Path(args.out_dir),
        rg_bin=args.rg_bin,
        repo_root=Path(args.repo_root) if args.repo_root else None,
    )
    print(json.dumps(rep, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
