#!/usr/bin/env python3
"""Own a harness process group and release its leased state after shutdown."""

from __future__ import annotations

import math
import os
from pathlib import Path
import secrets
import signal
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from verification_batch import child_options


def signal_group(pgid: int, sig: int) -> bool:
    try:
        os.killpg(pgid, sig)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        if sig == 0:
            return True
        # A group containing only zombies needs no further signal.
        processes = subprocess.check_output(["ps", "-axo", "pgid=,stat="], text=True)
        for row in processes.splitlines():
            group, state = row.split()
            if int(group) == pgid and not state.startswith("Z"):
                raise
        return False


def stop_stack(child: subprocess.Popen) -> None:
    signal_group(child.pid, signal.SIGTERM)
    deadline = time.monotonic() + 5
    while signal_group(child.pid, 0) and time.monotonic() < deadline:
        child.poll()
        time.sleep(0.1)
    signal_group(child.pid, signal.SIGKILL)
    child.wait()


def release_state(state: Path, token: str, env: dict[str, str], created: bool) -> int:
    marker = state / ".harness-lease"
    try:
        lease = marker.read_text().splitlines()
    except FileNotFoundError:
        if created:
            try:
                state.rmdir()
            except OSError:
                pass
        return 0
    if len(lease) < 2 or lease[1] != token:
        return 0
    cleanup = '''source "$1"
HARNESS_LEASE_TOKEN="$3"
bash "${SCRIPTS}/e2e-sidecar-stop.sh"
if [[ "${HARNESS_KEEP_STATE:-0}" == "1" ]]; then
  harness_release_state "$2"
else
  harness_remove_leased_state "$2" "$3"
fi'''
    return subprocess.run(
        ["bash", "-euc", cleanup, "harness-cleanup", str(Path(__file__).with_name("lib.sh")),
         str(state), token], env=env, check=False,
    ).returncode


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: supervise.py SCRIPT [ARGS...]", file=sys.stderr)
        return 2
    try:
        timeout = float(os.environ.get("LYCAON_HARNESS_TIMEOUT_SECONDS", "10800"))
        if not math.isfinite(timeout) or timeout < 0:
            raise ValueError
    except ValueError:
        print("error: LYCAON_HARNESS_TIMEOUT_SECONDS must be nonnegative and finite", file=sys.stderr)
        return 2

    owner = os.getppid()
    interrupted = 0

    def on_signal(sig: int, _frame: object) -> None:
        nonlocal interrupted
        interrupted = sig

    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, on_signal)

    env = os.environ.copy()
    created = not env.get("LYCAON_E2E_STATE_DIR")
    state = Path(env.get("LYCAON_E2E_STATE_DIR") or tempfile.mkdtemp(prefix="lycaon-harness.")).resolve()
    token = secrets.token_hex(24)
    env.update(LYCAON_E2E_STATE_DIR=str(state), HARNESS_SUPERVISOR_PID=str(os.getpid()),
               HARNESS_SUPERVISOR_TOKEN=token)
    child = None
    status = 1
    cleanup_status = 0
    try:
        child = subprocess.Popen(["bash", sys.argv[1], "--supervised", *sys.argv[2:]],
                                 env=env, **child_options())
        deadline = time.monotonic() + timeout if timeout else math.inf
        while True:
            if interrupted:
                status = 128 + interrupted
                break
            if os.getppid() != owner:
                print("harness: launcher exited; stopping the stack", file=sys.stderr)
                status = 143
                break
            result = child.poll()
            if result is not None:
                status = result if result >= 0 else 128 - result
                break
            if time.monotonic() >= deadline:
                print(f"harness: lifetime limit of {timeout:g}s reached; stopping the stack", file=sys.stderr)
                status = 124
                break
            time.sleep(0.1)
    finally:
        try:
            if child is not None:
                stop_stack(child)
        finally:
            cleanup_status = release_state(state, token, env, created)
    return status or cleanup_status


if __name__ == "__main__":
    raise SystemExit(main())
