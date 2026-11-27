#!/usr/bin/env python3
"""Host CPU-idle, load-average, and competing-process monitor for paired performance runs.

Supports both Linux (/proc/stat + /proc/loadavg) and macOS (iostat -w 1 + sysctl).
"""

import argparse
import json
import os
import platform
import re
import subprocess
import sys
import time
from datetime import datetime, timezone

COMPETING_PATTERNS = [
    r"\bgo\s+(test|build|run)\b",
    r"/pkg/tool/.+/(compile|link)\b",
    r"\bcargo\b",
    r"\brustc\b",
    r"\bclang(\+\+)?\b",
    r"\bgcc\b",
    r"\bcc1(plus)?\b",
    r"\bportable_command\b",
    r"\blycaon-dev\b",
    r"\bpaintedwolf-test-runs\b",
    r"\bpaintedwolf-source-snapshots\b",
    r"\bpytest\b",
    r"\bwasm-opt\b",
    r"\bwasm2c\b",
    r"\bmdworker_shared\b",
]
COMPETING_RE = re.compile("|".join(COMPETING_PATTERNS))


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def logical_cpus() -> int:
    return os.cpu_count() or 1


def read_loadavg() -> list[float]:
    try:
        la = os.getloadavg()
        return [round(la[0], 2), round(la[1], 2), round(la[2], 2)]
    except OSError:
        return [0.0, 0.0, 0.0]


def linux_cpu_times() -> tuple[int, int, int]:
    with open("/proc/stat", "r", encoding="utf-8") as fh:
        parts = fh.readline().split()
    vals = [int(x) for x in parts[1:]]
    idle = vals[3]
    iowait = vals[4] if len(vals) > 4 else 0
    total = sum(vals)
    return idle, iowait, total


def sample_cpu_1s() -> dict:
    sys_name = platform.system()
    if sys_name == "Linux":
        i0, w0, t0 = linux_cpu_times()
        time.sleep(1.0)
        i1, w1, t1 = linux_cpu_times()
        dt = max(1, t1 - t0)
        idle_pct = round(100.0 * (i1 - i0) / dt, 2)
        iowait_pct = round(100.0 * (w1 - w0) / dt, 2)
        busy_pct = round(100.0 - idle_pct - iowait_pct, 2)
        return {
            "ts_utc": utc_now(),
            "idle_pct": idle_pct,
            "iowait_pct": iowait_pct,
            "busy_pct": max(0.0, busy_pct),
            "loadavg": read_loadavg(),
        }

    # macOS: iostat -w 1 -c 2 -> second data line is the 1-second delta; last 6 cols are us sy id 1m 5m 15m
    proc = subprocess.run(
        ["iostat", "-w", "1", "-c", "2"],
        capture_output=True,
        text=True,
        check=True,
    )
    data_lines = []
    for line in proc.stdout.splitlines():
        toks = line.split()
        if len(toks) >= 6:
            try:
                float(toks[-1])
                float(toks[-2])
                float(toks[-3])
                int(toks[-4])
                int(toks[-5])
                int(toks[-6])
                data_lines.append(toks)
            except ValueError:
                continue
    if len(data_lines) < 2:
        raise RuntimeError(f"unexpected iostat output:\n{proc.stdout}")
    last = data_lines[-1]
    us = float(last[-6])
    sy = float(last[-5])
    idle = float(last[-4])
    la = [float(last[-3]), float(last[-2]), float(last[-1])]
    return {
        "ts_utc": utc_now(),
        "idle_pct": idle,
        "user_pct": us,
        "sys_pct": sy,
        "busy_pct": round(us + sy, 2),
        "loadavg": la,
    }


def list_descendant_pids(root_pids: set[int]) -> set[int]:
    if not root_pids:
        return set()
    sys_name = platform.system()
    cmd = ["ps", "-eo", "pid,ppid"] if sys_name == "Linux" else ["ps", "-Ao", "pid,ppid"]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    children: dict[int, list[int]] = {}
    for line in proc.stdout.splitlines()[1:]:
        parts = line.split()
        if len(parts) >= 2 and parts[0].isdigit() and parts[1].isdigit():
            pid, ppid = int(parts[0]), int(parts[1])
            children.setdefault(ppid, []).append(pid)
    seen = set(root_pids)
    stack = list(root_pids)
    while stack:
        cur = stack.pop()
        for ch in children.get(cur, []):
            if ch not in seen:
                seen.add(ch)
                stack.append(ch)
    return seen


def scan_competing_processes(ignore_pids: set[int]) -> list[dict]:
    sys_name = platform.system()
    cmd = ["ps", "-eo", "pid,pcpu,user,args"] if sys_name == "Linux" else ["ps", "-Ao", "pid,pcpu,user,args"]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    ignored = list_descendant_pids(ignore_pids | {os.getpid(), os.getppid()})
    found = []
    for line in proc.stdout.splitlines()[1:]:
        parts = line.strip().split(None, 3)
        if len(parts) < 4 or not parts[0].isdigit():
            continue
        pid = int(parts[0])
        if pid in ignored:
            continue
        try:
            pcpu = float(parts[1])
        except ValueError:
            pcpu = 0.0
        user = parts[2]
        args = parts[3]
        if "monitor_host.py" in args or "run-paired.sh" in args or "TestPY10_PairedBaseline" in args or "/provjobd" in args:
            continue
        # Report any matched build/qual process, or any non-agent process burning >= 50% CPU
        matched_pattern = bool(COMPETING_RE.search(args))
        if (matched_pattern and pcpu >= 0.5) or pcpu >= 50.0:
            found.append({
                "pid": pid,
                "pcpu": pcpu,
                "user": user,
                "matched_build_or_qual": matched_pattern,
                "args": args[:240],
            })
    found.sort(key=lambda item: item["pcpu"], reverse=True)
    return found


def summarize_samples(samples: list[dict], min_idle_req: float, competing_events: list[dict]) -> dict:
    idles = [s["idle_pct"] for s in samples]
    mean_idle = round(sum(idles) / len(idles), 2) if idles else 0.0
    min_idle = round(min(idles), 2) if idles else 0.0
    max_idle = round(max(idles), 2) if idles else 0.0
    sorted_idles = sorted(idles)
    med_idle = round(sorted_idles[len(sorted_idles) // 2], 2) if sorted_idles else 0.0
    return {
        "sample_count": len(samples),
        "mean_idle_pct": mean_idle,
        "median_idle_pct": med_idle,
        "min_idle_pct": min_idle,
        "max_idle_pct": max_idle,
        "required_min_idle_pct": min_idle_req,
        "competing_process_events": len(competing_events),
    }


def cmd_precheck(args: argparse.Namespace) -> int:
    ignore = {int(p) for p in args.ignore_pid}
    samples = []
    competing_events = []
    start_ts = utc_now()
    for i in range(args.duration):
        s = sample_cpu_1s()
        samples.append(s)
        if i % 5 == 0 or i == args.duration - 1:
            comp = scan_competing_processes(ignore)
            if comp:
                competing_events.append({"ts_utc": s["ts_utc"], "processes": comp})
    summary = summarize_samples(samples, args.min_idle, competing_events)
    idle_ok = summary["mean_idle_pct"] >= args.min_idle
    competing_ok = len(competing_events) == 0
    contaminated = not (idle_ok and competing_ok)
    reasons = []
    if not idle_ok:
        reasons.append(
            f"pre-series idle below threshold: mean={summary['mean_idle_pct']}% min={summary['min_idle_pct']}% (required >= {args.min_idle}%)"
        )
    if not competing_ok:
        reasons.append(f"detected competing processes in {len(competing_events)} scan(s)")

    payload = {
        "phase": "precheck",
        "hostname": platform.node(),
        "platform": f"{platform.system().lower()}-{platform.machine()}",
        "logical_cpus": logical_cpus(),
        "started_at": start_ts,
        "ended_at": utc_now(),
        "duration_s": args.duration,
        "contaminated": contaminated,
        "contamination_reasons": reasons,
        "summary": summary,
        "samples": samples,
        "competing_events": competing_events,
    }
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2)
        fh.write("\n")

    status = "CONTAMINATED" if contaminated else "CLEAN"
    print(
        f"PRECHECK {status}: cpus={payload['logical_cpus']} mean_idle={summary['mean_idle_pct']}% "
        f"min_idle={summary['min_idle_pct']}% load={samples[-1]['loadavg'] if samples else []} "
        f"competing_events={len(competing_events)}"
    )
    if reasons:
        print("  reasons:", "; ".join(reasons))
    return 1 if contaminated else 0


def cmd_watch(args: argparse.Namespace) -> int:
    ignore = {int(p) for p in args.ignore_pid}
    samples = []
    competing_events = []
    start_ts = utc_now()
    i = 0
    while not os.path.exists(args.stop_file):
        s = sample_cpu_1s()
        samples.append(s)
        if i % 3 == 0:
            comp = scan_competing_processes(ignore)
            if comp:
                competing_events.append({"ts_utc": s["ts_utc"], "processes": comp})
        i += 1
        if args.max_seconds > 0 and i >= args.max_seconds:
            break

    summary = summarize_samples(samples, 0.0, competing_events)
    contaminated = len(competing_events) > 0
    reasons = []
    if competing_events:
        reasons.append(f"detected competing processes during series in {len(competing_events)} scan(s)")

    payload = {
        "phase": "during",
        "hostname": platform.node(),
        "platform": f"{platform.system().lower()}-{platform.machine()}",
        "logical_cpus": logical_cpus(),
        "started_at": start_ts,
        "ended_at": utc_now(),
        "contaminated": contaminated,
        "contamination_reasons": reasons,
        "summary": summary,
        "samples": samples,
        "competing_events": competing_events,
    }
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2)
        fh.write("\n")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)

    p_pre = sub.add_parser("precheck")
    p_pre.add_argument("--duration", type=int, default=30)
    p_pre.add_argument("--min-idle", type=float, default=90.0)
    p_pre.add_argument("--out", required=True)
    p_pre.add_argument("--ignore-pid", action="append", default=[])

    p_w = sub.add_parser("watch")
    p_w.add_argument("--stop-file", required=True)
    p_w.add_argument("--out", required=True)
    p_w.add_argument("--max-seconds", type=int, default=600)
    p_w.add_argument("--ignore-pid", action="append", default=[])

    args = ap.parse_args()
    if args.cmd == "precheck":
        return cmd_precheck(args)
    return cmd_watch(args)


if __name__ == "__main__":
    sys.exit(main())
