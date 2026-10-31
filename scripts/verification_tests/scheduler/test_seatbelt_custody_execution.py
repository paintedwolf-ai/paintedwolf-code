"""Process identity and lease custody fallbacks when setugid tools are blocked."""

import importlib.util
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

import verification_health as health
import verification_lifecycle.custody as custody
import verification_lifecycle.terminal as lifetime
from verification_health import owner_identity
from verification_resources import Reservation, entries as reservations

SCRIPT = Path(__file__).resolve().parents[2] / "test-execution.py"
spec = importlib.util.spec_from_file_location("seatbelt_custody_execution", SCRIPT)
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)


@unittest.skipIf(os.name == "nt", "requires inherited POSIX capability locks")
class SeatbeltCustodyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.queue = execution.Queue(self.root / "queue")
        self.sequence = 0

    def capability(self, suffix="resource", batch="shared"):
        self.sequence += 1
        path = self.queue.root / f"fixture-{self.sequence}.{suffix}"
        file = path.open("x+")
        execution.lock_file(file)
        entry = {"ticket": path.stem, "kind": "invocation", "state": "running", "name": "fixture",
                 "pid": os.getpid(), "owner_identity": owner_identity(os.getpid()), "batch": batch,
                 "locks": ["fixture"], "shared_locks": [], "workers": 1}
        capability = lifetime.Capability(path, file, entry)
        capability.write()
        self.addCleanup(lambda: capability.file.close())
        return capability

    def test_missing_recorded_owner_identity_still_settles_for_same_live_pid(self):
        cap = self.capability()
        cap.entry["owner_identity"] = None
        cap.write()
        child = subprocess.Popen([sys.executable, "-c", "pass"], pass_fds=(cap.file.fileno(),))
        child.wait(timeout=10)
        self.assertFalse(lifetime.TerminalSettlement(self.queue, cap, child).finish())
        self.assertEqual(cap.entry["terminal_settlement"]["state"], "settled")

    @unittest.skipUnless(sys.platform == "darwin", "Darwin sysctl process sampling")
    def test_darwin_sysctl_fallback_matches_ps_identity_when_ps_is_blocked(self):
        pid = os.getpid()
        ps_identity = health.process_snapshot(pid)[pid]["identity"]
        with patch.object(subprocess, "run", side_effect=PermissionError("Operation not permitted")):
            sysctl_single = health.process_snapshot(pid)
            sysctl_all = health.process_snapshot()
        self.assertEqual(sysctl_single[pid]["identity"], ps_identity)
        self.assertEqual(sysctl_all[pid]["identity"], ps_identity)
        self.assertEqual(sysctl_single[pid]["parent"], os.getppid())
        self.assertEqual(health.ownership(pid, ps_identity, sysctl_single), "present")

    def test_full_reservation_to_settlement_lifecycle_under_seatbelt_ps_lsof_block(self):
        real_run = subprocess.run

        def deny_setugid_tools(cmd, *args, **kwargs):
            binary = Path(cmd[0]).name if cmd else ""
            if binary == "lsof" or (sys.platform == "darwin" and binary == "ps"):
                raise PermissionError("Operation not permitted (Seatbelt)")
            return real_run(cmd, *args, **kwargs)

        desc_ready = self.root / "seatbelt-desc-ready"
        ready = self.root / "seatbelt-ready.json"
        release_parent = self.root / "seatbelt-release-parent"
        stop_descendant = self.root / "seatbelt-stop-descendant"
        self.addCleanup(release_parent.touch)
        self.addCleanup(stop_descendant.touch)

        with patch.object(subprocess, "run", side_effect=deny_setugid_tools), \
                patch.object(lifetime, "DRAIN_SECONDS", .01), patch.object(lifetime, "SIGNAL_SECONDS", .2):
            spec = {"locks": ["fixture"], "workers": 1}
            with Reservation(self.queue, execution.lock_file, "batch-seatbelt", "fixture", spec, 1) as reservation:
                self.assertTrue(reservation.try_acquire())
                inherited = (reservation.file.fileno(),)
                descendant_code = (
                    "import os,time\nfrom pathlib import Path\n"
                    f"Path({str(desc_ready)!r}).write_text(str(os.getpid()))\n"
                    f"while not Path({str(stop_descendant)!r}).exists(): time.sleep(.01)\n"
                )
                server_code = (
                    "import json,os,socket,subprocess,sys,time\nfrom pathlib import Path\n"
                    "sock=socket.socket(socket.AF_INET,socket.SOCK_STREAM)\n"
                    "sock.bind(('127.0.0.1',0))\n"
                    "sock.listen(1)\n"
                    f"proc=subprocess.Popen([sys.executable,'-c',{descendant_code!r}],pass_fds={inherited!r},"
                    "start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n"
                    "desc_pid=None\n"
                    "for _ in range(1000):\n"
                    f"    if Path({str(desc_ready)!r}).exists():\n"
                    f"        raw=Path({str(desc_ready)!r}).read_text().strip()\n"
                    "        if raw:\n"
                    "            desc_pid=int(raw)\n"
                    "            break\n"
                    "    time.sleep(.01)\n"
                    f"Path({str(ready)!r}).write_text(json.dumps({{'port':sock.getsockname()[1],'descendant_pid':desc_pid}}))\n"
                    f"while not Path({str(release_parent)!r}).exists(): time.sleep(.01)\n"
                    "sock.close()\n"
                )
                child = subprocess.Popen(
                    [sys.executable, "-c", server_code],
                    pass_fds=inherited,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                )
                self.addCleanup(lambda: child.kill() if child.poll() is None else None)

                for _ in range(1000):
                    if ready.exists():
                        try:
                            info_payload = json.loads(ready.read_text())
                            if isinstance(info_payload, dict) and "descendant_pid" in info_payload:
                                break
                        except ValueError:
                            pass
                    threading.Event().wait(.01)
                else:
                    self.fail("child TCP server did not become ready")

                descendant_pid = int(info_payload["descendant_pid"])
                self.assertGreater(int(info_payload["port"]), 0)

                reservation.entry["child_pid"] = child.pid
                reservation.entry["child_identity"] = health.identity(child.pid)
                reservation.write()

                payload = {"runs": [reservation.entry], "operations": []}
                health.annotate(self.queue, payload)
                self.assertEqual(reservation.entry["health"]["owner"], "present")
                self.assertEqual(reservation.entry["health"]["child"], "present")
                self.assertIn(reservation.entry["health"]["state"], {"observing", "active"})

                stat_info = reservation.path.stat()
                expected_dev_ino = (stat_info.st_dev, stat_info.st_ino)
                self.assertTrue(custody.holds(child.pid, reservation.path, expected_dev_ino))
                self.assertTrue(custody.holds(descendant_pid, reservation.path, expected_dev_ino))
                self.assertIsNotNone(custody.birth(child.pid))
                self.assertIsNotNone(custody.birth(descendant_pid))
                holders = custody.lease_holders(reservation.path)
                self.assertIn(descendant_pid, holders)

                release_parent.touch()
                child.wait(timeout=10)
                self.assertEqual(child.returncode, 0)

                self.assertFalse(lifetime.TerminalSettlement(self.queue, reservation, child).finish())
                self.assertFalse(custody.holds(descendant_pid, reservation.path, expected_dev_ino))
                self.assertEqual(reservation.entry["terminal_settlement"]["state"], "settled")
                self.assertIn(descendant_pid, [r["pid"] for r in reservation.entry["terminal_settlement"]["reclaimed"]])

            with self.queue.locked():
                self.assertEqual(reservations(self.queue, execution.lock_file), [])

    def test_darwin_sysctl_snapshot_and_fallback_branches(self):
        health._darwin_libc.cache_clear()
        self.addCleanup(health._darwin_libc.cache_clear)
        with patch.object(health.ctypes.util, "find_library", return_value="libc.dylib"), \
                patch.object(health.ctypes, "CDLL", return_value="fake-cdll"):
            self.assertEqual(health._darwin_libc(), "fake-cdll")
        health._darwin_libc.cache_clear()
        with patch.object(health.ctypes.util, "find_library", return_value=None):
            self.assertIsNone(health._darwin_libc())
            with self.assertRaisesRegex(OSError, "unavailable"):
                health._darwin_process_snapshot(10)

        raw = bytearray(health._DARWIN_KINFO_PROC_SIZE * 2)
        second = health._DARWIN_KINFO_PROC_SIZE
        struct.pack_into("<q", raw, second, 1_700_000_000)
        struct.pack_into("<b", raw, second + 36, 2)
        struct.pack_into("<i", raw, second + 40, 4242)
        struct.pack_into("<i", raw, second + 560, 101)
        raw[second + 243:second + 251] = b"python3\x00"

        class FakeLibc:
            def __init__(self, first_rc=0, second_rc=0, taskinfo_rc=health._DARWIN_PROC_TASKINFO_SIZE):
                self.first_rc = first_rc
                self.second_rc = second_rc
                self.taskinfo_rc = taskinfo_rc

            def proc_pidinfo(self, _pid, _flavor, _arg, buf, _size):
                if self.taskinfo_rc == health._DARWIN_PROC_TASKINFO_SIZE:
                    buf[16:32] = struct.pack("<QQ", 65_250_000_000, 250_000_000)
                return self.taskinfo_rc

            def sysctl(self, _mib, _mib_len, buf, size_ptr, _newp, _newlen):
                if buf is None:
                    size_ptr._obj.value = len(raw) if self.first_rc == 0 else 0
                    return self.first_rc
                size_ptr._obj.value = len(raw)
                buf[:len(raw)] = bytes(raw)
                return self.second_rc

        self.assertEqual(health._darwin_cpu_time(FakeLibc(taskinfo_rc=0), 4242), "0:00.00")
        self.assertEqual(health._darwin_cpu_time(FakeLibc(), 4242), "1:05.50")

        with patch.object(health, "_darwin_libc", return_value=FakeLibc(first_rc=-1)):
            with self.assertRaisesRegex(OSError, "sampling failed"):
                health._darwin_process_snapshot(4242)
        with patch.object(health, "_darwin_libc", return_value=FakeLibc(second_rc=-1)):
            with self.assertRaisesRegex(OSError, "sampling failed"):
                health._darwin_process_snapshot(4242)
        with patch.object(health, "_darwin_libc", return_value=FakeLibc()):
            with self.assertRaisesRegex(OSError, "sampling failed"):
                health._darwin_process_snapshot(9999)

        with patch.object(health, "_darwin_libc", return_value=FakeLibc()), \
                patch.object(health.sys, "platform", "darwin"):
            with patch.object(subprocess, "run", side_effect=PermissionError("Seatbelt")):
                records = health.process_snapshot(4242)
            self.assertEqual(records[4242]["parent"], 101)
            self.assertEqual(records[4242]["state"], "R")
            self.assertEqual(records[4242]["cpu"], "1:05.50")
            self.assertEqual(records[4242]["command"], "python3")
            with patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "")):
                all_records = health.process_snapshot()
            self.assertIn(4242, all_records)
            self.assertNotIn(0, all_records)

        with patch.object(health.sys, "platform", "linux"), \
                patch.object(subprocess, "run", side_effect=PermissionError("denied")):
            with self.assertRaisesRegex(OSError, "sampling failed"):
                health.process_snapshot(4242)

    def test_custody_and_owner_fallback_branches_across_platforms(self):
        lease = self.root / "fixture.lease"
        lease.write_text("{}")
        stat_info = lease.stat()
        expected = (stat_info.st_dev, stat_info.st_ino)

        with patch.object(custody, "_darwin_libc", return_value=None):
            with self.assertRaisesRegex(OSError, "Lease holder inspection failed"):
                custody._darwin_lease_holders(lease)

        class FakeLibc:
            def __init__(self, first_rc=12, second_rc=12):
                self.first_rc = first_rc
                self.second_rc = second_rc

            def proc_listpidspath(self, _type, _info, _path, _flags, buf, _size):
                if buf is None:
                    return self.first_rc
                if self.second_rc >= 0:
                    buf[0] = 111
                    buf[1] = 0
                    buf[2] = 222
                return self.second_rc

        with patch.object(custody, "_darwin_libc", return_value=FakeLibc(first_rc=-1)):
            with self.assertRaisesRegex(OSError, "Lease holder inspection failed"):
                custody._darwin_lease_holders(lease)
        with patch.object(custody, "_darwin_libc", return_value=FakeLibc(second_rc=-1)):
            with self.assertRaisesRegex(OSError, "Lease holder inspection failed"):
                custody._darwin_lease_holders(lease)
        with patch.object(custody, "_darwin_libc", return_value=FakeLibc()):
            self.assertEqual(custody._darwin_lease_holders(lease), {111, 222})

        with patch.object(custody.Path, "is_dir", return_value=False):
            with patch.object(custody, "_darwin_libc", return_value=FakeLibc()), \
                    patch.object(custody.sys, "platform", "darwin"), \
                    patch.object(custody.subprocess, "run", side_effect=PermissionError("Seatbelt")):
                self.assertEqual(custody.lease_holders(lease), {111, 222})
                self.assertTrue(custody.holds(111, lease, expected))
                self.assertFalse(custody.holds(999, lease, expected))

            with patch.object(custody.sys, "platform", "linux"), \
                    patch.object(custody.subprocess, "run", return_value=subprocess.CompletedProcess([], 2, "")):
                with self.assertRaisesRegex(OSError, "Lease holder inspection failed"):
                    custody.lease_holders(lease)
                with self.assertRaisesRegex(OSError, "Process capability inspection failed"):
                    custody.holds(111, lease, expected)

            with patch.object(custody.subprocess, "run",
                              return_value=subprocess.CompletedProcess([], 0, "111\n222\n")):
                self.assertEqual(custody.lease_holders(lease), {111, 222})
                self.assertTrue(custody.holds(111, lease, expected))

        self.assertEqual(custody.commands(set()), {})
        with patch.object(custody.subprocess, "run", side_effect=PermissionError("Seatbelt")):
            with patch.object(custody.sys, "platform", "linux"):
                with self.assertRaisesRegex(OSError, "Process identity inspection failed"):
                    custody.commands({111})

            def fake_snapshot(pid):
                if pid == 111:
                    raise OSError("exited")
                return {222: {"command": "/usr/local/bin/python3"}}

            with patch.object(custody.sys, "platform", "darwin"), \
                    patch.object(custody, "_darwin_process_snapshot", side_effect=fake_snapshot):
                self.assertEqual(custody.commands({111, 222}), {222: "python3"})

        self.assertFalse(lifetime._same_live_owner({"pid": -1, "owner_identity": "any"}))
        self.assertTrue(lifetime._same_live_owner({"pid": os.getpid(), "owner_identity": None}))


if __name__ == "__main__":
    unittest.main()
