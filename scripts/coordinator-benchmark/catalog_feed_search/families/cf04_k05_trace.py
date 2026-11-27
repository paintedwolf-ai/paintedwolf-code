"""CF04 · K05 OS-level operation-trace observer & small-fixture controls (Decisions 053 & 055, D6-compliant)."""

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
# 12345 newfstatat(AT_FDCWD</cwd>, "/tmp/fix/g000/d000/f000.go", {...}, 0) = 0
# 12345 statx(14</tmp/fix/g000/d000>, "f000.go", ...) = -1 ENOENT (No such file or directory)
_UNFINISHED_RE = re.compile(r"^(\d+)\s+(.+)\s+<unfinished \.\.\.>$")
_RESUMED_RE = re.compile(r"^(\d+)\s+<\.\.\.\s+\S+\s+resumed>(.*)$")
_GETDENTS_RE = re.compile(r"getdents64\(\d+<([^>]+)>,.*=\s*(-?\d+)")
_FSTATAT_FD_RE = re.compile(r"(?:newfstatat|fstatat64|statx)\(\d+<([^>]+)>,\s*\"([^\"]*)\".*=\s*(-?\d+)")
_FSTATAT_CWD_RE = re.compile(r"(?:newfstatat|fstatat64|statx)\(AT_FDCWD(?:<[^>]*>)?,\s*\"([^\"]+)\".*=\s*(-?\d+)")


def _reassemble_strace_lines(strace_text: str) -> list[str]:
    """Reassemble concurrent `<unfinished ...>` / `<... syscall resumed>` pairs per TID."""
    pending: dict[str, str] = {}
    out: list[str] = []
    for raw in strace_text.splitlines():
        line = raw.strip()
        if not line:
            continue
        mu = _UNFINISHED_RE.match(line)
        if mu:
            pending[mu.group(1)] = f"{mu.group(1)} {mu.group(2)}"
            continue
        mr = _RESUMED_RE.match(line)
        if mr and mr.group(1) in pending:
            combined = pending.pop(mr.group(1)) + mr.group(2)
            out.append(combined)
            continue
        out.append(line)
    return out


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
    known_file_rels: set[str] | None = None,
) -> dict[str, object]:
    """Parse `strace -f -y` output scoped strictly to fixture_root (Decisions 053 & 055).

    - Reassembles `<unfinished ...>` / `<... resumed>` concurrent syscalls per TID.
    - Separates productive `getdents64` (`ret > 0`), EOF (`ret == 0`), and failed (`ret < 0`).
    - Matches successful stat calls (`ret == 0`) against `known_file_rels` and `known_dir_rels`,
      accounting unsuccessful (`ret < 0`) or unknown-path lookups (e.g. `.git` marker probes) separately.
    - Returns `status="unknown"` if the trace is empty OR contains zero fixture-scoped syscalls
      (preventing unrelated or missing traces from masquerading as zero reads).
    """
    lines = _reassemble_strace_lines(strace_text)
    if not lines:
        return {
            "status": "unknown",
            "reason": "empty strace output",
            "productive_dir_read_calls": "unknown",
            "eof_dir_read_calls": "unknown",
            "failed_dir_read_calls": "unknown",
            "distinct_directories_read": "unknown",
            "per_file_metadata_reads": "unknown",
            "distinct_known_files_statted": "unknown",
            "known_directory_metadata_reads": "unknown",
            "unsuccessful_or_unknown_lookups": "unknown",
        }

    root_path = Path(fixture_root)
    root_str = str(root_path.resolve()) if root_path.exists() else os.path.normpath(str(root_path))
    dirs_known = set(known_dir_rels) if known_dir_rels is not None else set()
    dirs_known.add(".")
    files_known = set(known_file_rels) if known_file_rels is not None else None

    fixture_scoped_events = 0
    productive_dir_reads = 0
    eof_dir_reads = 0
    failed_dir_reads = 0
    distinct_dirs: set[str] = set()

    known_file_stats = 0
    distinct_known_files: set[str] = set()
    known_dir_stats = 0
    unsuccessful_or_unknown = 0

    for line in lines:
        mg = _GETDENTS_RE.search(line)
        if mg:
            dir_path, ret_val = mg.group(1), int(mg.group(2))
            rel = _under_root(dir_path, root_str)
            if rel is not None:
                fixture_scoped_events += 1
                if ret_val > 0:
                    productive_dir_reads += 1
                    distinct_dirs.add(rel)
                elif ret_val == 0:
                    eof_dir_reads += 1
                else:
                    failed_dir_reads += 1
            continue

        rel_stat: str | None = None
        ret_stat: int | None = None
        mf = _FSTATAT_FD_RE.search(line)
        if mf:
            base_dir, name, ret_stat = mf.group(1), mf.group(2), int(mf.group(3))
            full = base_dir if name in ("", ".") else os.path.join(base_dir, name)
            rel_stat = _under_root(full, root_str)
        else:
            mc = _FSTATAT_CWD_RE.search(line)
            if mc:
                rel_stat = _under_root(mc.group(1), root_str)
                ret_stat = int(mc.group(2))

        if rel_stat is not None and ret_stat is not None:
            fixture_scoped_events += 1
            if ret_stat < 0:
                unsuccessful_or_unknown += 1
            elif rel_stat in dirs_known:
                known_dir_stats += 1
            elif files_known is None or rel_stat in files_known:
                known_file_stats += 1
                distinct_known_files.add(rel_stat)
            else:
                unsuccessful_or_unknown += 1

    if fixture_scoped_events == 0:
        return {
            "status": "unknown",
            "reason": "no fixture-scoped syscalls observed in trace",
            "trace_lines": len(lines),
            "productive_dir_read_calls": "unknown",
            "eof_dir_read_calls": "unknown",
            "failed_dir_read_calls": "unknown",
            "distinct_directories_read": "unknown",
            "per_file_metadata_reads": "unknown",
            "distinct_known_files_statted": "unknown",
            "known_directory_metadata_reads": "unknown",
            "unsuccessful_or_unknown_lookups": "unknown",
        }

    return {
        "status": "ok",
        "trace_lines": len(lines),
        "fixture_scoped_events": fixture_scoped_events,
        "productive_dir_read_calls": productive_dir_reads,
        "eof_dir_read_calls": eof_dir_reads,
        "failed_dir_read_calls": failed_dir_reads,
        "distinct_directories_read": len(distinct_dirs),
        "per_file_metadata_reads": known_file_stats,
        "distinct_known_files_statted": len(distinct_known_files),
        "known_directory_metadata_reads": known_dir_stats,
        "unsuccessful_or_unknown_lookups": unsuccessful_or_unknown,
    }


def _collect_inventory_rels(root: Path) -> tuple[set[str], set[str]]:
    dirs: set[str] = {"."}
    files: set[str] = set()
    for p in root.rglob("*"):
        rel = p.relative_to(root).as_posix()
        if p.is_dir():
            dirs.add(rel)
        elif p.is_file():
            files.add(rel)
    return dirs, files


_FSU_OPEN_RE = re.compile(r"\b(?:open|open_nocancel|openat|openat_nocancel)\b.*\bF=(\d+)\b")
_FSU_CLOSE_RE = re.compile(r"\b(?:close|close_nocancel)\b.*\bF=(\d+)\b")
_FSU_GETDIR_RE = re.compile(r"\bgetdirentries64\b.*\bF=(\d+)\b.*\bB=0x([0-9a-fA-F]+)\b")
_FSU_STAT_RE = re.compile(r"\b(lstat64|stat64|fstatat64|getattrlist|getattrlistat)\b")
_FSU_ERR_RE = re.compile(r"\[\s*(\d+)\s*\]")


def parse_darwin_fs_usage_ops(
    fs_usage_text: str,
    fixture_root: Path,
    *,
    known_dir_rels: set[str] | None = None,
    known_file_rels: set[str] | None = None,
) -> dict[str, object]:
    """Parse macOS `sudo -n fs_usage -w -f filesys` output scoped strictly to fixture_root (Decision 055)."""
    lines = [ln for ln in fs_usage_text.splitlines() if ln.strip()]
    if not lines:
        return {
            "status": "unknown",
            "reason": "empty fs_usage output",
            "productive_dir_read_calls": "unknown",
            "eof_dir_read_calls": "unknown",
            "failed_dir_read_calls": "unknown",
            "distinct_directories_read": "unknown",
            "per_file_metadata_reads": "unknown",
            "distinct_known_files_statted": "unknown",
            "known_directory_metadata_reads": "unknown",
            "unsuccessful_or_unknown_lookups": "unknown",
        }

    root_path = Path(fixture_root)
    root_abs = str(root_path.resolve()) if root_path.exists() else os.path.normpath(str(root_path))
    root_no_lead = root_abs.lstrip("/")
    dirs_known = set(known_dir_rels) if known_dir_rels is not None else set()
    dirs_known.add(".")
    files_known = set(known_file_rels) if known_file_rels is not None else None

    def _extract_rel(line: str) -> str | None:
        idx = line.find(root_no_lead)
        if idx < 0:
            return None
        tail = line[idx:].strip()
        # fs_usage wide lines end with `<timestamp> <proc.tid>` or whitespace padding; split on 2+ spaces
        path_part = re.split(r"\s{2,}", tail, maxsplit=1)[0].strip()
        return _under_root("/" + path_part.lstrip("/"), root_abs)

    fd_to_rel: dict[str, str] = {}
    fixture_scoped_events = 0
    productive_dir_reads = 0
    eof_dir_reads = 0
    failed_dir_reads = 0
    distinct_dirs: set[str] = set()
    known_file_stats = 0
    distinct_known_files: set[str] = set()
    known_dir_stats = 0
    unsuccessful_or_unknown = 0

    for line in lines:
        m_close = _FSU_CLOSE_RE.search(line)
        if m_close:
            fd_to_rel.pop(m_close.group(1), None)
            continue

        m_open = _FSU_OPEN_RE.search(line)
        if m_open:
            rel = _extract_rel(line)
            if rel is not None:
                fd_to_rel[m_open.group(1)] = rel
            else:
                fd_to_rel.pop(m_open.group(1), None)
            continue

        m_gd = _FSU_GETDIR_RE.search(line)
        if m_gd:
            fd, bytes_hex = m_gd.group(1), m_gd.group(2)
            rel = fd_to_rel.get(fd)
            if rel is not None:
                fixture_scoped_events += 1
                err_m = _FSU_ERR_RE.search(line)
                nbytes = int(bytes_hex, 16)
                if err_m:
                    failed_dir_reads += 1
                elif nbytes > 0:
                    productive_dir_reads += 1
                    distinct_dirs.add(rel)
                else:
                    eof_dir_reads += 1
            continue

        if _FSU_STAT_RE.search(line):
            rel = _extract_rel(line)
            if rel is not None:
                fixture_scoped_events += 1
                if _FSU_ERR_RE.search(line):
                    unsuccessful_or_unknown += 1
                elif rel in dirs_known:
                    known_dir_stats += 1
                elif files_known is None or rel in files_known:
                    known_file_stats += 1
                    distinct_known_files.add(rel)
                else:
                    unsuccessful_or_unknown += 1

    if fixture_scoped_events == 0:
        return {
            "status": "unknown",
            "reason": "no fixture-scoped fs_usage events observed",
            "trace_lines": len(lines),
            "productive_dir_read_calls": "unknown",
            "eof_dir_read_calls": "unknown",
            "failed_dir_read_calls": "unknown",
            "distinct_directories_read": "unknown",
            "per_file_metadata_reads": "unknown",
            "distinct_known_files_statted": "unknown",
            "known_directory_metadata_reads": "unknown",
            "unsuccessful_or_unknown_lookups": "unknown",
        }

    return {
        "status": "ok",
        "trace_lines": len(lines),
        "fixture_scoped_events": fixture_scoped_events,
        "productive_dir_read_calls": productive_dir_reads,
        "eof_dir_read_calls": eof_dir_reads,
        "failed_dir_read_calls": failed_dir_reads,
        "distinct_directories_read": len(distinct_dirs),
        "per_file_metadata_reads": known_file_stats,
        "distinct_known_files_statted": len(distinct_known_files),
        "known_directory_metadata_reads": known_dir_stats,
        "unsuccessful_or_unknown_lookups": unsuccessful_or_unknown,
    }


def _run_darwin_fs_usage_controls(
    *,
    engine_bin: Path,
    d1_worktree: Path,
    d1_commit: str,
    d1_tree: str,
    out_dir: Path,
    rg_bin: str,
    harness_root: Path,
) -> dict[str, object]:
    """Run macOS `sudo -n fs_usage -w -f filesys` small-fixture positive/negative controls (Decision 055)."""
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="cf04-fsu-") as tmp_s:
        tmp = Path(tmp_s)
        ctrl_tree = tmp / "ctrl-tree"
        ctrl_shape = write_scalefixture_tree(ctrl_tree, 20)
        ctrl_dirs, ctrl_files = _collect_inventory_rels(ctrl_tree)

        def _trace_py(code: str, duration_s: int = 4) -> str:
            p = subprocess.Popen([sys.executable, "-c", code, str(ctrl_tree)])
            fu = subprocess.run(
                ["sudo", "-n", "fs_usage", "-w", "-f", "filesys", "-t", str(duration_s), str(p.pid)],
                capture_output=True,
                text=True,
                check=False,
            )
            p.wait(timeout=10)
            return fu.stdout

        # Control A: scandir-only (directory reads, 0 per-file stats)
        code_a = (
            "import os, sys, time; time.sleep(1.8)\n"
            "def walk(d):\n"
            "  with os.scandir(d) as it:\n"
            "    for e in it:\n"
            "      if e.is_dir(follow_symlinks=False): walk(e.path)\n"
            "walk(sys.argv[1]); time.sleep(0.5)\n"
        )
        obs_a = parse_darwin_fs_usage_ops(_trace_py(code_a), ctrl_tree, known_dir_rels=ctrl_dirs, known_file_rels=ctrl_files)
        ctrl_a_pass = (
            obs_a["status"] == "ok"
            and obs_a["distinct_directories_read"] == ctrl_shape.total_directories
            and obs_a["per_file_metadata_reads"] == 0
        )

        # Control B: scandir + lstat on every file (directory reads + 20 per-file stats)
        code_b = (
            "import os, sys, time; time.sleep(1.8)\n"
            "def walk(d):\n"
            "  with os.scandir(d) as it:\n"
            "    for e in it:\n"
            "      if e.is_dir(follow_symlinks=False): walk(e.path)\n"
            "      else: os.lstat(e.path)\n"
            "walk(sys.argv[1]); time.sleep(0.5)\n"
        )
        obs_b = parse_darwin_fs_usage_ops(_trace_py(code_b), ctrl_tree, known_dir_rels=ctrl_dirs, known_file_rels=ctrl_files)
        ctrl_b_pass = (
            obs_b["status"] == "ok"
            and obs_b["distinct_directories_read"] == ctrl_shape.total_directories
            and obs_b["per_file_metadata_reads"] == 20
            and obs_b["distinct_known_files_statted"] == 20
        )

        # Control C & D: negative controls (empty & unrelated lines -> unknown)
        obs_c = parse_darwin_fs_usage_ops("", ctrl_tree, known_dir_rels=ctrl_dirs, known_file_rels=ctrl_files)
        obs_d = parse_darwin_fs_usage_ops(
            "23:15:46.182 lstat64   private/etc/hosts   0.000010 python3.1\n",
            ctrl_tree,
            known_dir_rels=ctrl_dirs,
            known_file_rels=ctrl_files,
        )
        ctrl_c_pass = obs_c["status"] == "unknown"
        ctrl_d_pass = obs_d["status"] == "unknown"

        report = {
            "platform": "Darwin",
            "observer": "sudo -n fs_usage -w -f filesys -t <sec> <pid>",
            "controls": {
                "control_a_dir_reads_only": {"passed": ctrl_a_pass, "expected_dirs": ctrl_shape.total_directories, "expected_file_stats": 0, "observed": obs_a},
                "control_b_dir_plus_file_stats": {"passed": ctrl_b_pass, "expected_dirs": ctrl_shape.total_directories, "expected_file_stats": 20, "observed": obs_b},
                "control_c_empty_trace_unknown": {"passed": ctrl_c_pass, "observed": obs_c},
                "control_d_unrelated_trace_unknown": {"passed": ctrl_d_pass, "observed": obs_d},
                "all_controls_passed": ctrl_a_pass and ctrl_b_pass and ctrl_c_pass and ctrl_d_pass,
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
            {"family": "K05-trace-controls-darwin", "all_controls_passed": report["controls"]["all_controls_passed"]},
            replay_command=["python3", "-m", "catalog_feed_search.families.cf04_k05_trace", "--out-dir", "."],
        )
        return report


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
    """Run Decision-053/055 positive/negative controls + real D1 sidecar small-fixture strace proof on Linux."""
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    harness_root = Path(repo_root).resolve() if repo_root else Path(__file__).resolve().parents[4]

    if platform.system().lower() == "darwin":
        return _run_darwin_fs_usage_controls(
            engine_bin=engine_bin,
            d1_worktree=d1_worktree,
            d1_commit=d1_commit,
            d1_tree=d1_tree,
            out_dir=out,
            rg_bin=rg_bin,
            harness_root=harness_root,
        )
    if platform.system().lower() != "linux":
        res = {
            "platform": platform.system(),
            "status": "unknown",
            "reason": "Linux strace observer is Linux-only; use macOS fs_usage control on Darwin",
        }
        res_path = out / "k05_trace_controls.json"
        res_path.write_text(json.dumps(res, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return res

    with tempfile.TemporaryDirectory(prefix="cf04-k05-trace-") as tmp_s:
        tmp = Path(tmp_s)
        ctrl_tree = tmp / "ctrl-tree"
        ctrl_shape = write_scalefixture_tree(ctrl_tree, 20)
        ctrl_dirs, ctrl_files = _collect_inventory_rels(ctrl_tree)

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
        obs_a = parse_strace_fixture_ops(
            trace_a.read_text(encoding="utf-8", errors="replace"),
            ctrl_tree,
            known_dir_rels=ctrl_dirs,
            known_file_rels=ctrl_files,
        )
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
        obs_b = parse_strace_fixture_ops(
            trace_b.read_text(encoding="utf-8", errors="replace"),
            ctrl_tree,
            known_dir_rels=ctrl_dirs,
            known_file_rels=ctrl_files,
        )
        ctrl_b_pass = (
            obs_b["status"] == "ok"
            and obs_b["distinct_directories_read"] == ctrl_shape.total_directories
            and obs_b["per_file_metadata_reads"] == 20
            and obs_b["distinct_known_files_statted"] == 20
        )

        # Control C: negative control — empty trace -> unknown
        obs_c = parse_strace_fixture_ops("", ctrl_tree, known_dir_rels=ctrl_dirs, known_file_rels=ctrl_files)
        ctrl_c_pass = obs_c["status"] == "unknown"

        # Control D: negative control — trace with ONLY unrelated lines -> unknown (Decision 055)
        obs_d = parse_strace_fixture_ops(
            '101 getdents64(5</etc>, 0x1, 8192) = 128\n101 newfstatat(AT_FDCWD</>, "/etc/hosts", {}, 0) = 0\n',
            ctrl_tree,
            known_dir_rels=ctrl_dirs,
            known_file_rels=ctrl_files,
        )
        ctrl_d_pass = obs_d["status"] == "unknown"

        # 2. Real D1 sidecar under strace on a 50-file scalefixture (fixture built BEFORE strace starts!)
        sidecar_tree = tmp / "sidecar-tree"
        sidecar_shape = write_scalefixture_tree(sidecar_tree, 50)
        sidecar_dirs, sidecar_files = _collect_inventory_rels(sidecar_tree)
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
            start_new_session=True,
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
            _, _, summary = wait_catalog_settled(client.driver, pid, rid, max_wait_s=20.0, poll_interval_s=0.1)
            settled_file_count = int(summary.get("file_count", -1))
        finally:
            with contextlib.suppress(OSError):
                os.killpg(proc.pid, signal.SIGINT)
            try:
                proc.wait(timeout=8.0)
            except subprocess.TimeoutExpired:
                with contextlib.suppress(OSError):
                    os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=3.0)
                except subprocess.TimeoutExpired:
                    with contextlib.suppress(OSError):
                        os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait(timeout=3.0)

        obs_d1 = parse_strace_fixture_ops(
            trace_d1.read_text(encoding="utf-8", errors="replace") if trace_d1.is_file() else "",
            sidecar_tree,
            known_dir_rels=sidecar_dirs,
            known_file_rels=sidecar_files,
        )

        report = {
            "platform": platform.system(),
            "observer": "/usr/bin/strace -f -y -e trace=getdents64,newfstatat,statx",
            "controls": {
                "control_a_dir_reads_only": {"passed": ctrl_a_pass, "expected_dirs": ctrl_shape.total_directories, "expected_file_stats": 0, "observed": obs_a},
                "control_b_dir_plus_file_stats": {"passed": ctrl_b_pass, "expected_dirs": ctrl_shape.total_directories, "expected_file_stats": 20, "observed": obs_b},
                "control_c_empty_trace_unknown": {"passed": ctrl_c_pass, "observed": obs_c},
                "control_d_unrelated_trace_unknown": {"passed": ctrl_d_pass, "observed": obs_d},
                "all_controls_passed": ctrl_a_pass and ctrl_b_pass and ctrl_c_pass and ctrl_d_pass,
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
                "all_controls_passed": ctrl_a_pass and ctrl_b_pass and ctrl_c_pass and ctrl_d_pass,
                "d1_observed": obs_d1,
            },
            replay_command=["python3", "-m", "catalog_feed_search.families.cf04_k05_trace", "--out-dir", "."],
        )
        return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run Decision-053/055 K05 strace operation-trace controls")
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
