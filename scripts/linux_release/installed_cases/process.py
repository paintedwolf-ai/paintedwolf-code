"""Run only retained final native images with bounded evidence and owned cancellation."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import platform
import signal
import stat
import subprocess
import threading

from ..installed_runtime import unchanged
from scripts.harness.supervise import stop_stack

LIMIT = 8 << 20


def identity(pid: int) -> dict:
    row = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    if len(row) < 20 or int(row[1]) != os.getpid():
        raise ValueError("native probe launch identity differs from its captured child")
    return {"pid": pid, "start_time_ticks": int(row[19])}


def run(binding: dict, role: str, arguments: list[str], *, timeout=120) -> dict:
    if platform.system() != "Linux":
        raise ValueError("installed native process probes require Linux")
    path = unchanged(binding, role)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    process = None
    try:
        info = os.fstat(descriptor)
        expected = binding["images"][str(path)]
        digest = hashlib.sha256()
        while block := os.read(descriptor, 1 << 20):
            digest.update(block)
        if (digest.hexdigest(), info.st_size, stat.S_IMODE(info.st_mode), info.st_dev, info.st_ino) != (
                expected["sha256"], expected["bytes"], expected["mode"], expected["device"], expected["inode"]):
            raise ValueError("native probe image changed before descriptor execution")
        environment = {"HOME": str(Path.home()), "PATH": "/run/current-system/sw/bin:/usr/bin:/bin", "LC_ALL": "C"}
        process = subprocess.Popen([str(path), *arguments], executable=f"/proc/self/fd/{descriptor}",
            pass_fds=(descriptor,), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            env=environment, start_new_session=True)
        launch_identity = identity(process.pid)
        overflow = threading.Event()
        streams = {"stdout": bytearray(), "stderr": bytearray()}

        def kill_owned_group():
            if process.poll() is None:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass

        def capture(name, stream):
            with stream:
                while block := stream.read(64 << 10):
                    if len(streams[name]) + len(block) > LIMIT:
                        overflow.set()
                        kill_owned_group()
                        return
                    streams[name].extend(block)

        readers = [threading.Thread(target=capture, args=(name, getattr(process, name)), daemon=True)
                   for name in streams]
        for reader in readers:
            reader.start()
        timed_out = False
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            kill_owned_group()
            process.wait()
        for reader in readers:
            reader.join(timeout=2)
        if any(reader.is_alive() for reader in readers):
            stop_stack(process)
            for reader in readers:
                reader.join(timeout=2)
            raise ValueError("native probe left a descendant holding its evidence channels")
        unchanged(binding, role)
        return {"launch_identity": launch_identity, "exit_code": process.returncode, "timed_out": timed_out, "output_limit": overflow.is_set(),
                **{name: bytes(data) for name, data in streams.items()}}
    finally:
        os.close(descriptor)
        if process is not None:
            stop_stack(process)
