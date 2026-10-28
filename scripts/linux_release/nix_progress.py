"""Keep daemon-hosted Nix build activity visible to verification admission."""
from __future__ import annotations

from pathlib import Path
import subprocess
import sys
import threading


def run(command, *, cwd, env):
    destination = env.get("PW_TEST_STAGE_PROGRESS")
    if not destination:
        return subprocess.run(command, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE)
    # Nix's stdout remains its machine-readable result; stderr is the build log.
    with Path(destination).open("ab", buffering=0) as progress:
        with subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE) as process:
            errors = []

            def copy_log():
                while chunk := process.stderr.read1(64 << 10):
                    if errors:
                        continue
                    try:
                        progress.write(chunk)
                        sys.stderr.write(chunk.decode("utf-8", errors="replace"))
                        sys.stderr.flush()
                    except (OSError, ValueError) as error:
                        errors.append(error)

            reader = threading.Thread(target=copy_log)
            reader.start()
            try:
                output = process.stdout.read()
                status = process.wait()
            finally:
                reader.join()
            if errors:
                raise errors[0]
    return subprocess.CompletedProcess(command, status, output.decode("utf-8"))
