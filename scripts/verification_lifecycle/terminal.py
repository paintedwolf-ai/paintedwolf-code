"""Settle one terminal producer's retained capability without losing crash custody."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

import verification_lifecycle.custody as custody
from verification_health import ownership, process_snapshot

if os.name != "nt":
    import fcntl

DRAIN_SECONDS = 0.1
SIGNAL_SECONDS = 2


def retained(entry):
    """A live settling owner guards admission while a capability is probed/reacquired."""
    state = entry.get("terminal_settlement", {}).get("state")
    if state not in {"settling", "blocked"}:
        return False
    try:
        return ownership(entry.get("pid"), entry.get("owner_identity"), process_snapshot()) not in {"exited", "replaced"}
    except (OSError, ValueError, subprocess.SubprocessError):
        return True


class Capability:
    """An already-held scheduler lease; this introduces no independent authority."""

    def __init__(self, path, file, entry):
        self.path, self.file, self.entry = Path(path), file, entry

    def write(self):
        body = json.dumps(self.entry)
        body = body.ljust(max(len(body), os.fstat(self.file.fileno()).st_size))
        self.file.seek(0)
        self.file.write(body)
        self.file.flush()


class TerminalSettlement:
    def __init__(self, queue, capability, child):
        if child.poll() is None:
            raise ValueError("A producer must be reaped before terminal settlement")
        self.queue, self.capability, self.child = queue, capability, child
        info = os.fstat(capability.file.fileno())
        self.expected = info.st_dev, info.st_ino
        self.tainted = False

    def probe(self):
        """Queue lock + live-owner guard cover the entire retire/reacquire interval."""
        cap = self.capability
        with self.queue.locked():
            if cap.entry.get("pid") != os.getpid() or ownership(
                    cap.entry.get("pid"), cap.entry.get("owner_identity"), process_snapshot(os.getpid())) != "present":
                raise OSError("Terminal capability owner identity is unavailable")
            replacement = cap.path.open("r+")
            info = os.fstat(replacement.fileno())
            if (info.st_dev, info.st_ino) != self.expected:
                replacement.close()
                raise OSError("Terminal capability identity changed")
            cap.file.close()
            cap.file = replacement
            try:
                fcntl.flock(cap.file, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return False
            return True

    def wait_release(self, seconds):
        deadline = time.monotonic() + seconds
        while True:
            if self.probe():
                return True
            if time.monotonic() >= deadline:
                return False
            time.sleep(0.05)

    def signal_holders(self, signum):
        with self.queue.locked():
            cap = self.capability
            if cap.entry.get("pid") != os.getpid() or ownership(
                    cap.entry.get("pid"), cap.entry.get("owner_identity"), process_snapshot(os.getpid())) != "present":
                raise OSError("Terminal capability owner identity is unavailable")
            pids = custody.lease_holders(cap.path) - {os.getpid()}
            if not pids:
                # The last holder may have exited after the contended probe.
                # A still-held uninspectable capability will fail the bounded wait.
                return
            names = custody.commands(pids)
            records = []
            for pid in sorted(pids):
                created = custody.birth(pid)
                if created is None:
                    continue
                if custody.signal_holder(pid, created, cap.path, self.expected, signum):
                    records.append({"pid": pid, "basename": names.get(pid, ""), "birth": created,
                                    "signal": signal.Signals(signum).name})
            cap.entry["terminal_settlement"].setdefault("reclaimed", []).extend(records)
            cap.write()

    def blocked(self, error, on_blocked):
        self.tainted = True
        cap = self.capability
        with self.queue.locked():
            cap.entry["terminal_settlement"].update(state="blocked", error=type(error).__name__, unverified=True)
            cap.write()
        print(json.dumps({"status": "unverified", "reason": "terminal_cleanup_blocked",
                          "ticket": cap.entry["ticket"]}), file=sys.stderr, flush=True)
        if on_blocked is not None:
            on_blocked()
        # Keep the original supervisor, admission guard and source descriptors alive.
        # A public cancellation can stop it; a holder's own exit can also restore custody.
        while True:
            try:
                if self.probe():
                    return
            except (OSError, ValueError, subprocess.SubprocessError):
                pass
            time.sleep(0.25)

    def finish(self, on_blocked=None):
        cap = self.capability
        if os.name == "nt":
            return False
        with self.queue.locked():
            cap.entry["terminal_settlement"] = {"state": "settling", "producer_pid": self.child.pid,
                                                  "producer_exit_code": self.child.returncode,
                                                  "producer_identity": cap.entry.get("child_identity")}
            cap.write()
        try:
            if not self.wait_release(DRAIN_SECONDS):
                for signum in (signal.SIGTERM, signal.SIGKILL):
                    self.signal_holders(signum)
                    if self.wait_release(SIGNAL_SECONDS):
                        break
                else:
                    raise OSError("Terminal capability remained held after bounded settlement")
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            self.blocked(error, on_blocked)
        with self.queue.locked():
            cap.entry["terminal_settlement"]["state"] = "settled"
            cap.write()
        return self.tainted
