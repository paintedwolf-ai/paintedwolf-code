#!/usr/bin/env python3
"""`go test -exec` program for shared verification stages.

Reports each package's outcome the moment its test binary exits, replays a recorded pass for an
identical build and identical observed inputs, and records new passes. Outside a shared stage it only
runs the binary.
"""

import os
from pathlib import Path
import re
import select
import signal
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verification_reuse as reuse  # noqa: E402
from ci_policy.resources import Monitor, budget  # noqa: E402
from ci_policy.quarantine import arguments as quarantine_arguments  # noqa: E402

CONTROL_ENV = ("PW_TEST_STAGE_EVENTS", "PW_TEST_REUSE_STORE", "PW_TEST_ENVIRONMENT_IDENTITY", "PW_TEST_PACKAGE_IDENTITIES",
               "PW_TEST_SCRATCH_ROOT", "PW_TEST_SOURCE_ROOT", "PW_TEST_MODULE_ROOT", "PW_TEST_STAGE_ID",
               "PW_TEST_BATCH_DIRECTORY")
# A test that leaks a process holding its output open cannot delay the package's outcome for long.
OUTPUT_GRACE_SECONDS = 5
FAILED_TEST = re.compile(r"^\s*--- FAIL: (\S+)")
MAX_FAILED_TESTS = 20


def write_all(data):
    view = memoryview(data)
    while view:
        view = view[os.write(1, view):]


def execute(binary, arguments, env, observe_forks, resource_limit=None):
    """Run the binary with combined output streamed through; returns (wait status, output, forked)."""
    output_read, output_write = os.pipe()
    ready_read, ready_write = os.pipe()
    pid = os.fork()
    if pid == 0:
        try:
            os.close(output_read)
            os.close(ready_write)
            # Fork events are watched from before the binary's first instruction.
            os.read(ready_read, 1)
            os.dup2(output_write, 1)
            os.dup2(output_write, 2)
            os.execve(binary, [binary, *arguments], env)
        finally:
            os._exit(127)
    os.close(output_write)
    os.close(ready_read)
    watch = None
    if observe_forks:
        watch = select.kqueue()
        watch.control([select.kevent(pid, filter=select.KQ_FILTER_PROC,
                                     flags=select.KQ_EV_ADD | select.KQ_EV_CLEAR,
                                     fflags=select.KQ_NOTE_FORK)], 0, 0)
    running = True
    monitor = Monitor(pid, resource_limit) if resource_limit and Path("/proc").is_dir() else None

    def forward(number, _frame):
        if running:
            os.kill(pid, number)

    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGQUIT, signal.SIGHUP):
        signal.signal(signum, forward)
    os.write(ready_write, b"x")
    os.close(ready_write)
    captured, forked, status, exited, open_output = bytearray(), False, None, None, True

    def drain_forks():
        nonlocal forked
        if watch is not None:
            for event in watch.control(None, 16, 0):
                forked = forked or bool(event.fflags & select.KQ_NOTE_FORK)

    while open_output or status is None:
        sources = [output_read] if open_output else []
        if watch is not None:
            sources.append(watch.fileno())
        try:
            readable, _, _ = select.select(sources, [], [], 0.2)
        except InterruptedError:
            readable = []
        if watch is not None and watch.fileno() in readable:
            drain_forks()
        if open_output and output_read in readable:
            chunk = os.read(output_read, 1 << 16)
            if chunk:
                write_all(chunk)
                if len(captured) <= reuse.MAX_OUTPUT_BYTES:
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
    drain_forks()
    return status, bytes(captured), (forked if watch is not None else None), monitor.violation if monitor else None


def failure_report(events, package, output):
    directory = Path(events).with_name(Path(events).name.removesuffix("-events.jsonl") + "-failures")
    directory.mkdir(exist_ok=True)
    path = directory / (package.replace("/", "__") + ".log")
    text = output.decode("utf-8", "replace").replace("\x16", "")
    path.write_text(text)
    tests = []
    for line in text.splitlines():
        match = FAILED_TEST.match(line)
        if match and match.group(1) not in tests:
            tests.append(match.group(1))
    return {"output": str(path), "tests": tests[:MAX_FAILED_TESTS]}


def main():
    binary, *arguments = sys.argv[1:]
    environment = dict(os.environ)
    child_environment = {key: value for key, value in environment.items() if key not in CONTROL_ENV}
    events = environment.get("PW_TEST_STAGE_EVENTS")
    if not events:
        os.execve(binary, [binary, *arguments], child_environment)
    context = reuse.Context.from_environment(environment)
    package = context.package(os.getcwd())
    arguments = quarantine_arguments(package, arguments, environment.get("PW_QUARANTINE_OBSERVE") == "1")
    store = Path(environment["PW_TEST_REUSE_STORE"]) if environment.get("PW_TEST_REUSE_STORE") else None
    environment["PW_PACKAGE_RESOURCE_IDENTITY"] = str(budget(package))
    key = reuse.result_key(arguments, package, environment) if store and reuse.fork_observable() else None
    started = time.monotonic()
    if key:
        hit = reuse.lookup(store, key, context, environment)
        if hit:
            output, provenance = hit
            write_all(output)
            reuse.emit(events, {"package": package, "exit_code": 0, "elapsed": round(time.monotonic() - started, 3),
                                "reused": provenance or {"recorded": True}})
            return 0
    log = None
    if key:
        log = Path(environment["PW_TEST_SCRATCH_ROOT"]) / "test-logs" / f"{os.getpid()}.log"
        log.parent.mkdir(exist_ok=True)
        arguments = [f"-test.testlogfile={log}", *arguments]
    status, output, forked, resource_limit = execute(binary, arguments, child_environment, reuse.fork_observable(), budget(package))
    elapsed = time.monotonic() - started
    code = os.waitstatus_to_exitcode(status)
    if resource_limit:
        code = 1
        diagnostic = (f"\npackage resource budget exceeded: {package}: "
                      f"{resource_limit['measured_bytes']} bytes RSS > {resource_limit['limit_bytes']}\n").encode()
        output += diagnostic
        write_all(diagnostic)
    event = {"package": package, "exit_code": code, "elapsed": round(elapsed, 3), "reused": False,
             "forked": forked, "recorded": False}
    if resource_limit:
        event["resource_limit"] = resource_limit
    if code != 0:
        event.update(failure_report(events, package, output))
    elif key and forked is False and log is not None and log.is_file():
        inputs = reuse.observed_inputs(log.read_text(errors="replace"), os.getcwd(), context)
        if inputs is not None:
            provenance = {"batch_id": Path(environment.get("PW_TEST_BATCH_DIRECTORY", "")).name,
                          "stage_id": environment.get("PW_TEST_STAGE_ID", ""), "recorded_at": time.time()}
            event["recorded"] = reuse.record(store, key, package, inputs, context, environment, output, elapsed,
                                             provenance)
            event["inputs"] = len(inputs)
    if log is not None:
        log.unlink(missing_ok=True)
    reuse.emit(events, event)
    if code < 0:
        signal.signal(-code, signal.SIG_DFL)
        os.kill(os.getpid(), -code)
    return code


if __name__ == "__main__":
    sys.exit(main())
