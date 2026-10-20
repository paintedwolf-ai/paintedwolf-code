"""Bounded read-only Git observations of one protected AUR package branch."""
from __future__ import annotations

import os
from pathlib import Path
import platform
import re
import resource
import selectors
import shutil
import signal
import stat
import subprocess
import time

from .aur_delivery import RECIPE_FILES

MAX_REPOSITORY = 64 << 20


def child_limits():
    resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_REPOSITORY, MAX_REPOSITORY))


class ReadOnlyOrigin:
    production = True

    def __init__(self, root: Path, package: str):
        if package not in {"painted-wolf-code-bin", "painted-wolf-code-preview-bin"}:
            raise ValueError("select the maintained channel-specific AUR origin")
        self.origin = "https://aur.archlinux.org/" + package + ".git"
        self.initialize(root)

    def initialize(self, root):
        if platform.system() != "Linux" or os.geteuid() == 0:
            raise ValueError("protected AUR Git observation requires a nonroot Linux controller")
        root = Path(root)
        parent = root.parent.lstat()
        if (not root.is_absolute() or root.parent.resolve(strict=True) != root.parent
                or not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid()
                or parent.st_mode & 0o022 or root.exists() or root.is_symlink()):
            raise ValueError("AUR observation requires a fresh private controller directory")
        root.mkdir(mode=0o700)
        self.root = root
        info = root.lstat()
        self.identity = (info.st_dev, info.st_ino)
        self.environment = {"PATH": os.environ["PATH"], "HOME": str(root), "LANG": "C", "LC_ALL": "C",
                            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
                            "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "/bin/false"}
        try:
            self.git("init", "--bare", "--initial-branch=master", str(root))
        except BaseException:
            self.close()
            raise

    def disk_bound(self):
        count, size = 0, 0
        for path in self.root.rglob("*"):
            count += 1
            info = path.lstat()
            if stat.S_ISLNK(info.st_mode):
                raise ValueError("Git observation produced an unexpected filesystem link")
            if stat.S_ISREG(info.st_mode):
                size += info.st_size
            if count > 4096 or size > MAX_REPOSITORY:
                raise ValueError("AUR observation exceeded its private repository bound")

    def git(self, *arguments):
        command = ["git", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=",
                   "-c", "http.followRedirects=false", "-c", "protocol.ext.allow=never",
                   "-c", "protocol.file.allow=" + ("never" if self.production else "always"),
                   "-C", str(self.root), *map(str, arguments)]
        process = subprocess.Popen(command, env=self.environment, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, start_new_session=True, preexec_fn=child_limits)
        streams = [bytearray(), bytearray()]
        deadline = time.monotonic() + 60
        try:
            with selectors.DefaultSelector() as selector:
                for index, stream in enumerate((process.stdout, process.stderr)):
                    selector.register(stream, selectors.EVENT_READ, index)
                while selector.get_map():
                    if time.monotonic() >= deadline:
                        raise ValueError("AUR Git observation exceeded its deadline")
                    self.disk_bound()
                    for key, _ in selector.select(timeout=0.05):
                        chunk = os.read(key.fileobj.fileno(), 65536)
                        if not chunk:
                            selector.unregister(key.fileobj)
                            continue
                        if len(streams[key.data]) + len(chunk) > 1 << 20:
                            raise ValueError("AUR Git observation exceeded its output bound")
                        streams[key.data].extend(chunk)
            while True:
                completed = os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
                if completed is not None:
                    if completed.si_code != os.CLD_EXITED or completed.si_status != 0:
                        raise ValueError("the fixed read-only AUR Git operation failed")
                    self.disk_bound()
                    return bytes(streams[0])
                if time.monotonic() >= deadline:
                    raise ValueError("AUR Git observation exceeded its deadline")
                time.sleep(0.01)
        finally:
            # The unreaped leader retains its PID/group identity through cleanup.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=10)
            process.stdout.close()
            process.stderr.close()

    def head(self):
        raw = self.git("ls-remote", "--exit-code", self.origin, "refs/heads/master")
        if re.fullmatch(rb"[0-9a-f]{40}\trefs/heads/master\n", raw) is None:
            raise ValueError("AUR origin returned a missing or ambiguous recipe branch")
        return raw[:40].decode("ascii")

    def read(self, commit):
        if re.fullmatch(r"[0-9a-f]{40}", commit) is None:
            raise ValueError("select an exact immutable AUR commit")
        self.git("fetch", "--depth=1", "--no-tags", self.origin, "refs/heads/master")
        if self.git("rev-parse", "--verify", "FETCH_HEAD").strip().decode("ascii") != commit:
            raise ValueError("AUR branch changed before its selected objects were fetched")
        rows = self.git("ls-tree", "-r", "-l", "-z", "--full-tree", commit)
        objects = {}
        for row in rows.split(b"\0"):
            if not row:
                continue
            match = re.fullmatch(rb"100644 blob ([0-9a-f]{40}) +([0-9]+)\t([^\0]+)", row)
            if match is None:
                raise ValueError("AUR recipe contains a nonregular declaration")
            blob, size, name = match.groups()
            name = name.decode("ascii")
            if name not in RECIPE_FILES or name in objects or int(size) > 1 << 20:
                raise ValueError("AUR recipe contains extra or oversized declarations")
            raw = self.git("cat-file", "blob", blob.decode("ascii"))
            if len(raw) != int(size):
                raise ValueError("AUR recipe blob differs from its committed size")
            objects[name] = raw
        return {"commit": commit, "message": self.git("show", "-s", "--format=%B", commit),
                "objects": objects}

    def close(self):
        info = self.root.lstat()
        if not stat.S_ISDIR(info.st_mode) or (info.st_dev, info.st_ino) != self.identity:
            raise ValueError("owned AUR observation directory changed before cleanup")
        shutil.rmtree(self.root)

    def __enter__(self):
        return self

    def __exit__(self, *_error):
        self.close()


class FixtureOrigin(ReadOnlyOrigin):
    """A local Git fixture has no production origin or publication authority."""
    production = False

    def __init__(self, root, origin):
        origin = Path(origin)
        info = origin.lstat()
        if (not origin.is_absolute() or origin.resolve(strict=True) != origin
                or not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o022):
            raise ValueError("select the physical owned local Git fixture")
        self.origin = str(origin)
        self.initialize(root)
