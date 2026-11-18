#!/usr/bin/env python3
"""`go test -exec` program for shared verification stages.

Reports each package's outcome the moment its test binary exits and holds it to its memory budget.
Outside a shared stage it only runs the binary.
"""

import json
import os
from pathlib import Path
import re
import select
import signal
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from ci_policy.resources import Monitor, budget, runtime_environment  # noqa: E402

CONTROL_ENV = ("PW_TEST_STAGE_EVENTS", "PW_TEST_RACE", "PW_TEST_MODULE_ROOT", "PW_TEST_STAGE_ID",
               "PW_TEST_BATCH_DIRECTORY")
# A test that leaks a process holding its output open cannot delay the package's outcome for long.
OUTPUT_GRACE_SECONDS = 5
# A test the watchdog found waiting is as much the failure as one that reported it.
FAILED_TEST = re.compile(r"^\s*--- FAIL: (\S+)|^pending wait: (\S+): ")
MAX_FAILED_TESTS = 20
MAX_OUTPUT_BYTES = 64 << 20


def write_all(data):
    view = memoryview(data)
    while view:
        view = view[os.write(1, view):]


def execute(binary, arguments, env, resource_limit=None):
    """Run the binary with combined output streamed through; returns (wait status, output, resource violation)."""
    output_read, output_write = os.pipe()
    ready_read, ready_write = os.pipe()
    pid = os.fork()
    if pid == 0:
        try:
            os.close(output_read)
            os.close(ready_write)
            # The parent's signal forwarding is in place before the binary starts.
            os.read(ready_read, 1)
            os.dup2(output_write, 1)
            os.dup2(output_write, 2)
            os.execve(binary, [binary, *arguments], env)
        finally:
            os._exit(127)
    os.close(output_write)
    os.close(ready_read)
    running = True
    monitor = Monitor(pid, resource_limit) if resource_limit and Path("/proc").is_dir() else None

    def forward(number, _frame):
        if running:
            os.kill(pid, number)

    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGQUIT, signal.SIGHUP, signal.SIGUSR1):
        signal.signal(signum, forward)
    os.write(ready_write, b"x")
    os.close(ready_write)
    captured, status, exited, open_output = bytearray(), None, None, True
    while open_output or status is None:
        sources = [output_read] if open_output else []
        try:
            readable, _, _ = select.select(sources, [], [], 0.2)
        except InterruptedError:
            readable = []
        if open_output and output_read in readable:
            chunk = os.read(output_read, 1 << 16)
            if chunk:
                write_all(chunk)
                if len(captured) <= MAX_OUTPUT_BYTES:
                    captured.extend(chunk)
            else:
                open_output = False
        if status is None:
            if monitor and monitor.sample():
                # The PID was forked above and has not been reaped, so it cannot have been reused.
                os.kill(pid, signal.SIGKILL)
            finished, code = os.waitpid(pid, os.WNOHANG)
            if finished:
                status, exited, running = code, time.monotonic(), False
        if status is not None and open_output and time.monotonic() - exited > OUTPUT_GRACE_SECONDS:
            open_output = False
    os.close(output_read)
    return status, bytes(captured), monitor.violation if monitor else None


def emit(events, value):
    line = (json.dumps(value, separators=(",", ":")) + "\n").encode()
    descriptor = os.open(events, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    try:
        os.write(descriptor, line)
    finally:
        os.close(descriptor)


def package_name(directory, module_root):
    """Import path of the package whose test binary runs in `directory`."""
    module = next((line.split()[1] for line in (Path(module_root) / "go.mod").read_text().splitlines()
                   if line.startswith("module ")), None)
    relative = Path(os.path.realpath(directory)).relative_to(os.path.realpath(module_root)).as_posix()
    return module if relative == "." else f"{module}/{relative}"


def failure_report(events, package, output):
    directory = Path(events).with_name(Path(events).name.removesuffix("-events.jsonl") + "-failures")
    directory.mkdir(exist_ok=True)
    path = directory / (package.replace("/", "__") + ".log")
    text = output.decode("utf-8", "replace").replace("\x16", "")
    path.write_text(text)
    tests = []
    for line in text.splitlines():
        match = FAILED_TEST.match(line)
        if match and (match.group(1) or match.group(2)) not in tests:
            tests.append(match.group(1) or match.group(2))
    return {"output": str(path), "tests": tests[:MAX_FAILED_TESTS]}


def main():
    binary, *arguments = sys.argv[1:]
    environment = dict(os.environ)
    child_environment = {key: value for key, value in environment.items() if key not in CONTROL_ENV}
    events = environment.get("PW_TEST_STAGE_EVENTS")
    if not events:
        os.execve(binary, [binary, *arguments], child_environment)
    package = package_name(os.getcwd(), environment["PW_TEST_MODULE_ROOT"])
    race = environment.get("PW_TEST_RACE") == "1"
    limit = budget(package, race)
    started = time.monotonic()
    status, output, resource_limit = execute(binary, arguments, runtime_environment(child_environment, limit), limit)
    elapsed = time.monotonic() - started
    code = os.waitstatus_to_exitcode(status)
    if resource_limit:
        code = 1
        diagnostic = (f"\npackage resource budget exceeded: {package}: "
                      f"{resource_limit['measured_bytes']} bytes RSS > {resource_limit['limit_bytes']}\n").encode()
        output += diagnostic
        write_all(diagnostic)
    event = {"package": package, "exit_code": code, "elapsed": round(elapsed, 3)}
    if resource_limit:
        event["resource_limit"] = resource_limit
    if code != 0:
        event.update(failure_report(events, package, output))
    emit(events, event)
    if code < 0:
        signal.signal(-code, signal.SIG_DFL)
        os.kill(os.getpid(), -code)
    return code


if __name__ == "__main__":
    sys.exit(main())
