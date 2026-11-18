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
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
import verification_reuse as reuse  # noqa: E402
from verification_lifecycle import custody  # noqa: E402
from ci_policy.resources import Monitor, budget, runtime_environment  # noqa: E402

CONTROL_ENV = ("PW_TEST_STAGE_EVENTS", "PW_TEST_REUSE_STORE", "PW_TEST_ENVIRONMENT_IDENTITY", "PW_TEST_PACKAGE_IDENTITIES",
               "PW_TEST_RACE",
               "PW_TEST_SCRATCH_ROOT", "PW_TEST_SOURCE_ROOT", "PW_TEST_MODULE_ROOT", "PW_TEST_STAGE_ID",
               "PW_TEST_BATCH_DIRECTORY")
# Every process a package starts inherits this entry unless it clears its environment.
OWNER_ENV = "PW_TEST_PACKAGE_OWNER"
# A test the watchdog found waiting is as much the failure as one that reported it.
FAILED_TEST = re.compile(r"^\s*--- FAIL: (\S+)|^pending wait: (\S+): ")
MAX_FAILED_TESTS = 20


def write_all(data):
    view = memoryview(data)
    while view:
        view = view[os.write(1, view):]


def execute(binary, arguments, env, owner, observe_forks, settle, resource_limit=None):
    """Run the binary with combined output streamed through.

    Returns (wait status, output, forked, resource violation, leaked processes). Once the binary exits, settle
    removes what it left behind, so a leaked holder of the output cannot delay the outcome.
    """
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
            # An inherited file identifies descendants even after they leave the group.
            os.set_inheritable(owner.fileno(), True)
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

    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGQUIT, signal.SIGHUP, signal.SIGUSR1):
        signal.signal(signum, forward)
    os.write(ready_write, b"x")
    os.close(ready_write)
    captured, forked, status, open_output = bytearray(), False, None, True

    def drain_forks():
        nonlocal forked
        if watch is not None:
            for event in watch.control(None, 16, 0):
                forked = forked or bool(event.fflags & select.KQ_NOTE_FORK)

    def read_output():
        nonlocal open_output
        chunk = os.read(output_read, 1 << 16)
        if not chunk:
            open_output = False
            return
        write_all(chunk)
        if len(captured) <= reuse.MAX_OUTPUT_BYTES:
            captured.extend(chunk)

    while status is None:
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
            read_output()
        if monitor and monitor.sample():
            # The PID was forked above and has not been reaped, so it cannot have been reused.
            os.kill(pid, signal.SIGKILL)
        finished, code = os.waitpid(pid, os.WNOHANG)
        if finished:
            status, running = code, False
    drain_forks()
    observed = forked if watch is not None else None
    leaked = settle() if observed is not False else {}
    # What the binary wrote is already in the pipe; anything still holding it open is not waited for.
    while open_output and select.select([output_read], [], [], 0)[0]:
        read_output()
    os.close(output_read)
    return status, bytes(captured), observed, monitor.violation if monitor else None, leaked


def settle_package_children(owner, entry):
    """Kill and name every live process that kept the package's file or environment entry."""
    stat = os.fstat(owner.fileno())
    expected = (stat.st_dev, stat.st_ino)
    found = (custody.lease_holders(owner.name) | custody.carriers(entry)) - {os.getpid()}
    births = {pid: custody.birth(pid) for pid in found}
    names = custody.commands(found)
    leaked = {}
    for pid, created in births.items():
        # A process gone by the time it is named exited on its own.
        if created is None or pid not in names or custody.birth(pid) != created:
            continue
        leaked[pid] = names[pid]
        if custody.carries(pid, entry) and custody.birth(pid) == created:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        else:
            custody.signal_holder(pid, created, owner.name, expected, signal.SIGKILL)
    return leaked


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
    context = reuse.Context.from_environment(environment)
    package = context.package(os.getcwd())
    store = Path(environment["PW_TEST_REUSE_STORE"]) if environment.get("PW_TEST_REUSE_STORE") else None
    race = environment.get("PW_TEST_RACE") == "1"
    environment["PW_PACKAGE_RESOURCE_IDENTITY"] = str(budget(package, race))
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
    limit = budget(package, race)
    entry = f"{OWNER_ENV}={os.getpid()}-{os.urandom(8).hex()}"
    scan = {}

    def settle():
        scan_started = time.monotonic()
        try:
            return settle_package_children(owner, entry)
        finally:
            scan["seconds"] = round(time.monotonic() - scan_started, 3)

    with tempfile.NamedTemporaryFile(prefix="package-child-", dir=environment["PW_TEST_SCRATCH_ROOT"]) as owner:
        status, output, forked, resource_limit, leaked = execute(
            binary, arguments, {**runtime_environment(child_environment, limit), OWNER_ENV: entry.partition("=")[2]},
            owner, reuse.fork_observable(), settle, limit)
    elapsed = time.monotonic() - started
    code = os.waitstatus_to_exitcode(status)
    if resource_limit:
        code = 1
        diagnostic = (f"\npackage resource budget exceeded: {package}: "
                      f"{resource_limit['measured_bytes']} bytes RSS > {resource_limit['limit_bytes']}\n").encode()
        output += diagnostic
        write_all(diagnostic)
    if leaked:
        code = 1
        leaked_names = ", ".join(f"{name} (pid {pid})" for pid, name in leaked.items())
        diagnostic = f"\npackage left child processes: {package}: {leaked_names}\n".encode()
        output += diagnostic
        write_all(diagnostic)
    event = {"package": package, "exit_code": code, "elapsed": round(elapsed, 3), "reused": False,
             "forked": forked, "recorded": False}
    if scan:
        event["child_scan_seconds"] = scan["seconds"]
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
