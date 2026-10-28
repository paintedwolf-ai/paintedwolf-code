"""Prepare pinned Python tools once for each interpreter and platform."""
import hashlib
import os
import shutil
import subprocess
import sys
import sysconfig

from artifact_paths import bin_dir


def directory(requirements):
    identity = hashlib.sha256(requirements.read_bytes() + sys.version.encode() + sysconfig.get_platform().encode())
    return bin_dir() / f"python-tools-{identity.hexdigest()[:16]}"


def prepared(requirements):
    target = directory(requirements)
    if not (target / "ready").is_file():
        raise ValueError("Python tools require setup-dev preparation")
    return target


def provision(requirements):
    """The pinned packages of one requirements file for this interpreter, installed once (the network once)."""
    target = directory(requirements)
    if (target / "ready").exists():
        return target
    target.parent.mkdir(parents=True, exist_ok=True)
    with (target.parent / f"{target.name}.lock").open("a") as lock:
        if os.name != "nt":
            import fcntl
            fcntl.flock(lock, fcntl.LOCK_EX)
        if not (target / "ready").exists():
            staging = target.with_name(f"{target.name}.{os.getpid()}")
            shutil.rmtree(staging, ignore_errors=True)
            subprocess.run([sys.executable, "-m", "pip", "install", "--disable-pip-version-check", "--no-input",
                            "--no-deps", "--target", str(staging), "-r", str(requirements)], check=True)
            shutil.rmtree(target, ignore_errors=True)
            os.replace(staging, target)
            (target / "ready").write_text(requirements.read_text())
    return target
