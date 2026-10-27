"""Measure compressed compiler payloads and retain reusable workspace dependencies."""
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile


def compressed_size(paths):
    if not paths:
        return 0
    with tempfile.TemporaryDirectory(prefix="cache-measure-", dir=os.environ.get("RUNNER_TEMP")) as temporary:
        manifest = Path(temporary) / "files"
        manifest.write_bytes(b"".join(os.fsencode(str(path).lstrip("/")) + b"\0" for path in sorted(paths)))
        tar = shutil.which("gtar") or shutil.which("tar")
        if not tar:
            raise ValueError("cache measurement requires tar")
        pipeline = '"$1" -cf - -C / --format=pax --no-recursion --null -T "$2" | zstd -q -T1 --long=30 -c | wc -c'
        process = subprocess.Popen(["bash", "-o", "pipefail", "-c", pipeline, "cache-measure", tar, str(manifest)],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True,
                                   env={**os.environ, "COPYFILE_DISABLE": "1"})
        try:
            output, error = process.communicate(timeout=600)
        except BaseException:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.communicate()
            raise
        if process.returncode:
            raise subprocess.CalledProcessError(process.returncode, process.args, output, error)
        return int(output.strip())


def prune_workspace(manifest):
    metadata = subprocess.run(["cargo", "metadata", "--manifest-path", str(manifest), "--no-deps", "--locked",
                               "--offline", "--format-version", "1"], capture_output=True, text=True, check=True)
    data = json.loads(metadata.stdout)
    members = set(data["workspace_members"])
    packages = {package["id"]: package["name"] for package in data["packages"]}
    if not members or not members.issubset(packages):
        raise ValueError("cache pruning requires complete workspace membership")
    command = ["cargo", "clean", "--manifest-path", str(manifest), "--locked", "--offline"]
    for member in sorted(members):
        command.extend(["--package", packages[member]])
    subprocess.run(command, check=True)


if __name__ == "__main__":
    prune_workspace(Path(sys.argv[1]))
