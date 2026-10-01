"""Bound delivered-code execution inside its isolated container."""
import subprocess
import sys

DEADLINE_SECONDS = 30
TIMEOUT_EXIT = 124


def run(program, timeout=DEADLINE_SECONDS):
    command = [sys.executable, '-I', '-B', '-c', program]
    try:
        return int(subprocess.run(command, timeout=timeout).returncode != 0)
    except subprocess.TimeoutExpired:
        return TIMEOUT_EXIT


if __name__ == '__main__':
    raise SystemExit(run(sys.argv[1]))
