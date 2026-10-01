#!/usr/bin/env python3
"""Coordinate local verification across checkouts and agents."""

import argparse
import contextlib
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import time
import uuid

from verification_advisories import admission_notice
from verification_history import record_duration
from verification_resources import (EXCLUSIVE, Reservation, blocked_reason, bounded_environment, order_of,
                                    worker_budget)
from verification_health import identity, owner_identity, report, SAMPLE_SECONDS

if os.name == "nt":
    import msvcrt
else:
    import fcntl


def lock_file(file, blocking=True):
    if os.name != "nt":
        fcntl.flock(file, fcntl.LOCK_EX | (0 if blocking else fcntl.LOCK_NB))
        return
    # Lock beyond the metadata so status readers can inspect an active lease.
    position = file.tell()
    file.seek(1 << 30)
    try:
        while True:
            try:
                msvcrt.locking(file.fileno(), msvcrt.LK_NBLCK, 1)
                return
            except OSError as error:
                if not blocking:
                    raise BlockingIOError() from error
                time.sleep(0.05)
    finally:
        file.seek(position)


CONTROL_TASKS = {"test:pause", "test:resume", "test:status", "test:cancel"}
SCHEDULED_NAMESPACES = {"build", "check", "check-fast", "test", "lint", "perf", "comments", "upgrade", "bundle"}
SCHEDULED_TASKS = {"default", "oar:conformance", "eval:tool-usage",
                   "den:bundle", "den:stage-engine", "den:harness:canary", "release:preflight"}
INFO_FLAGS = {"--help", "-h", "--list", "-l", "--list-all", "-a", "--version", "--summary", "--status", "--dry", "-n", "--completion", "--experiments"}
VALUE_FLAGS = {"-d", "--dir", "-t", "--taskfile", "-o", "--output", "--sort", "-I", "--interval", "-C", "--concurrency", "--output-group-begin", "--output-group-end", "--completion"}
TASK_BOOL_FLAGS = (INFO_FLAGS - {"--completion"}) | {
    "-c", "--color", "--disable-fuzzy", "-x", "--exit-code", "-F", "--failfast", "-f", "--force",
    "-g", "--global", "-i", "--init", "--insecure", "--interactive", "-j", "--json", "--nested",
    "--no-status", "--output-group-error-only", "-p", "--parallel", "-s", "--silent", "-v", "--verbose",
    "-w", "--watch", "-y", "--yes"}
TASK_INFO_ALIASES = {"-h": "--help", "-l": "--list", "-a": "--list-all", "-n": "--dry"}


def task_arguments(arguments):
    """Parse Task options once, keeping flag values out of control decisions."""
    names, variables, options, normalized = [], {}, {}, []
    index = 0
    while index < len(arguments):
        arg = arguments[index]
        if arg == "--":
            normalized.extend(arguments[index:])
            break
        if not arg.startswith("-"):
            normalized.append(arg)
            if "=" in arg:
                key, value = arg.split("=", 1)
                variables[key] = value
            else:
                names.append(arg)
            index += 1
            continue
        name, separator, value = arg.partition("=")
        if not arg.startswith("--") and len(name) > 2:
            short = name[:2]
            if short in VALUE_FLAGS:
                name, separator, value = short, "=", arg[2:]
            elif all("-" + letter in TASK_BOOL_FLAGS for letter in name[1:]) and not separator:
                for letter in name[1:]:
                    flag = "-" + letter
                    options[TASK_INFO_ALIASES.get(flag, flag)] = True
                    normalized.append(flag)
                index += 1
                continue
        if name in VALUE_FLAGS:
            if not separator:
                index += 1
                if index == len(arguments):
                    raise ValueError(f"Task option {name} requires a value")
                value = arguments[index]
            if name in {"-C", "--concurrency"}:
                try:
                    int(value)
                except ValueError:
                    raise ValueError(f"Task option {name} requires an integer") from None
            options[name] = value
            normalized.extend([name, value])
        elif name in TASK_BOOL_FLAGS:
            if separator and value not in {"true", "false", "1", "0", "t", "f", "TRUE", "FALSE", "True", "False", "T", "F"}:
                raise ValueError(f"Task option {name} requires a boolean")
            enabled = not separator or value in {"true", "1", "t", "TRUE", "True", "T"}
            options[TASK_INFO_ALIASES.get(name, name)] = enabled
            normalized.append(arg)
        else:
            raise ValueError(f"unsupported Task option {arg!r}; use ./task --help for supported options")
        index += 1
    return {"names": names, "variables": variables, "options": options, "arguments": normalized}


def task_names(arguments):
    return task_arguments(arguments)["names"]


def scheduled_task(arguments):
    parsed = task_arguments(arguments)
    if any(parsed["options"].get(flag) for flag in INFO_FLAGS):
        return False
    names = parsed["names"]
    if "den:harness" in names:
        if len(names) != 1:
            raise ValueError("run the interactive den:harness separately from other targets")
        cli = arguments[arguments.index("--") + 1:] if "--" in arguments else []
        if cli == ["--prepare"]:
            return True
    variables = parsed["variables"]
    benchmark = variables.get("BENCHMARK", os.environ.get("BENCHMARK", ""))
    if any(name in CONTROL_TASKS for name in names):
        if len(names) != 1:
            raise ValueError("run test pause, resume, and status controls separately")
        return False
    from verification_plan import catalog, declared_names
    declared = declared_names(catalog())
    return not names or any(
        name in declared or name in SCHEDULED_TASKS or name.split(":", 1)[0] in SCHEDULED_NAMESPACES
        or name.startswith(("den:test", "den:lint", "den:typecheck", "den:coverage", "den:harness:test", "e2e:"))
        for name in names if not (name == "eval:tool-usage" and benchmark not in {"", "test", "prepare", "smoke"})
    )


def validate_task_selection(arguments):
    from verification_plan import validate_selection
    separator = arguments.index("--") if "--" in arguments else len(arguments)
    validate_selection(task_names(arguments), arguments[separator + 1:])


ARGUMENT_PROBE = "PW_TASK_ARGUMENT_PROBE"


def probe_task_request(binary, root, arguments):
    """Task resolves names and CLI_ARGS only when it runs, so a request it would reject or narrow
    to nothing is dry-run here, before it waits for admission."""
    parsed = task_arguments(arguments)
    cli = arguments[arguments.index("--") + 1:] if "--" in arguments else []
    resolution = [*(f"{key}={value}" for key, value in parsed["variables"].items()),
                  *(item for name in ("-t", "--taskfile", "-d", "--dir") if name in parsed["options"]
                    for item in (name, parsed["options"][name]))]
    # Each target is probed alone so one that forwards arguments cannot hide one that drops them.
    for names in ([[name] for name in parsed["names"]] if cli and parsed["names"] else [parsed["names"]]):
        probe = subprocess.run([binary, "-d", root, *resolution, "--dry", "--force", *names,
                                *(["--", ARGUMENT_PROBE] if cli else [])],
                               stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
        output = probe.stdout + probe.stderr
        if probe.returncode:
            reasons = [line.removeprefix("task: ") for line in output.splitlines()
                       if line.startswith("task: ") and not line.startswith(("task: [", "task: Available"))]
            raise ValueError(f"{(reasons or output.strip().splitlines() or [f'Task exited {probe.returncode}'])[-1]}"
                             ". No verification was queued")
        if cli and ARGUMENT_PROBE not in output:
            raise ValueError(f"{names[0] if names else 'default'} passes nothing after -- to its commands; Task "
                             f"would drop {shlex.join(cli)} and run the whole target. No verification was queued")


def serial_task_arguments(arguments):
    result = []
    arguments = task_arguments(arguments)["arguments"]
    index = 0
    while index < len(arguments):
        arg = arguments[index]
        if arg == "--":
            result.extend(arguments[index:])
            break
        if arg in VALUE_FLAGS:
            if arg not in {"-C", "--concurrency"}:
                result.extend(arguments[index:index + 2])
            index += 2
        else:
            result.append(arg)
            index += 1
    return ["--concurrency", "1", *result]


def process_parents():
    if os.name == "nt":
        output = subprocess.check_output(["powershell", "-NoProfile", "-Command",
            "Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId | ConvertTo-Json -Compress"], text=True)
        return {entry["ProcessId"]: entry["ParentProcessId"] for entry in json.loads(output)}
    output = subprocess.check_output(["ps", "-axo", "pid=,ppid="], text=True)
    return {int(pid): int(parent) for pid, parent in (line.split() for line in output.splitlines())}


def is_descendant(pid, ancestor, parents):
    seen = set()
    while pid in parents and pid not in seen:
        if pid == ancestor:
            return True
        seen.add(pid)
        pid = parents[pid]
    return False


class Queue:
    lock_file = staticmethod(lock_file)

    def __init__(self, root):
        self.root = Path(root)
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        if os.name != "nt" and self.root.stat().st_uid != os.getuid():
            raise ValueError("test execution directory belongs to another user")
        self.root.chmod(0o700)

    @contextlib.contextmanager
    def locked(self):
        with (self.root / "queue.lock").open("a+") as lock:
            lock_file(lock)
            yield

    def entries(self):
        entries = []
        for path in self.root.glob("*.lease"):
            stale = False
            with path.open("r+") as lease:
                try:
                    lock_file(lease, blocking=False)
                except BlockingIOError:
                    entries.append(json.load(lease))
                else:
                    stale = True
                    from verification_batch import recover_abandoned
                    try:
                        entry = json.load(lease)
                    except json.JSONDecodeError:
                        entry = {}
                    recover_abandoned(entry)
            if stale:
                path.unlink()
                path.with_suffix(".batch.json").unlink(missing_ok=True)
        return sorted(entries, key=lambda entry: entry["ticket"])

    def pause(self, reason):
        with self.locked():
            (self.root / "paused.json").write_text(json.dumps({"reason": reason, "at": time.time()}))

    def resume(self):
        with self.locked():
            (self.root / "paused.json").unlink(missing_ok=True)

    def status(self):
        with self.locked():
            pause = self.root / "paused.json"
            from verification_resources import capacity, entries
            operations = entries(self, lock_file)
            reserved = sum(entry["workers"] for entry in operations if entry["state"] == "running")
            return {"paused": json.loads(pause.read_text()) if pause.exists() else None, "runs": self.entries(),
                    "capacity": {"workers": capacity(), "reserved": reserved, "available": max(0, capacity() - reserved)},
                    "operations": operations}

    def inherited(self):
        ticket = os.environ.get("PW_TEST_EXECUTION_TICKET", "")
        if not ticket:
            return False
        with self.locked():
            entry = next((entry for entry in self.entries() if entry["ticket"] == ticket), None)
        if not entry or entry["state"] != "running":
            return False
        from verification_batch import inherited_lease
        if inherited_lease(self, entry, lock_file):
            return True
        parents = process_parents()
        return any(is_descendant(os.getpid(), pid, parents)
                   for pid in (entry["pid"], entry.get("child_pid", 0),
                               *entry.get("operation_pids", {}).values()))


class Lease:
    def __init__(self, queue, name, workers, metadata=None):
        self.queue = queue
        self.entry = {"pid": os.getpid(), "owner_identity": owner_identity(os.getpid()),
                      "name": name, "source": os.getcwd(), "workers": workers,
                      "state": "queued", "queued_at": time.time()}
        self.entry.update(metadata or {})
        self.path = None
        self.file = None

    def __enter__(self):
        with self.queue.locked():
            entries = self.queue.entries()
            sequence = max((int(entry["ticket"].split("-", 1)[0]) for entry in entries), default=0) + 1
            self.entry["ticket"] = f"{sequence:020d}-{uuid.uuid4().hex}"
            self.path = self.queue.root / (self.entry["ticket"] + ".lease")
            self.file = self.path.open("x+")
            lock_file(self.file)
            self.write()
        return self

    def write(self):
        body = json.dumps(self.entry)
        # Padding overwrites longer metadata without truncating the lease.
        body = body.ljust(max(len(body), os.fstat(self.file.fileno()).st_size))
        self.file.seek(0)
        self.file.write(body)
        self.file.flush()

    def acquire(self, reservation):
        """An invocation is admitted when its reservation is: declared resources, FIFO per conflict."""
        last_notice = 0.0
        last_state = None
        while True:
            with self.queue.locked():
                paused = (self.queue.root / "paused.json").exists()
                from verification_resources import entries as reservations
                reason = "paused" if paused else blocked_reason(
                    reservation.entry, reservations(self.queue, lock_file))
                if reason is None:
                    started = time.time()
                    reservation.entry.update(state="running", started_at=started, blocked_on=None)
                    reservation.write()
                    self.entry.update(state="running", started_at=started)
                    self.write()
                    return
                reservation.entry["blocked_on"] = reason
                reservation.write()
            if reason != last_state:
                print(f"test execution: {self.entry['name']} queued ({reason})", file=sys.stderr, flush=True)
                if last_state is None:
                    print("test execution: wait on this command session for the result; no status polling is needed",
                          file=sys.stderr, flush=True)
                last_state = reason
            if time.monotonic() - last_notice >= SAMPLE_SECONDS:
                report(self.queue, self.entry["ticket"])
                last_notice = time.monotonic()
            time.sleep(0.25)

    def __exit__(self, *_):
        with self.queue.locked():
            self.file.close()
            # Descendants may still hold an inherited descriptor after the owner exits.
            self.queue.entries()


def bounded_go_arguments(arguments, environment):
    env = bounded_environment(environment, worker_budget(environment))
    limits = {"-p": int(env["GO_TEST_P"]), "-parallel": int(env["GO_TEST_PARALLEL"]),
              "-test.parallel": int(env["GO_TEST_PARALLEL"])}
    result = []
    index = 0
    while index < len(arguments):
        arg = arguments[index]
        if arg in {"-args", "--args"}:
            result.extend(arguments[index:])
            break
        name, separator, value = arg.partition("=")
        if name in {"-run", "-bench", "-skip", "-test.run", "-test.bench", "-test.skip"} and not separator:
            result.extend(arguments[index:index + 2])
            index += 2
            continue
        if name in limits:
            if not separator:
                index += 1
                if index == len(arguments):
                    raise ValueError(f"{name} requires a value")
                value = arguments[index]
            count = int(value)
            if count < 1:
                raise ValueError(f"{name} must be positive")
            result.append(f"{name}={min(count, limits[name])}")
        else:
            result.append(arg)
        index += 1
    return env, result


def run(queue, name, command, spec=EXCLUSIVE):
    """Run outside a shared batch, holding `spec` for the whole command; nested requests inherit it."""
    if queue.inherited():
        os.execvpe(command[0], command, os.environ)
    ceiling = worker_budget(os.environ)
    with Lease(queue, name, ceiling, {"kind": "invocation"}) as lease, \
            Reservation(queue, lock_file, lease.entry["ticket"], name, spec, ceiling,
                        order_of(lease.entry)) as reservation:
        workers = reservation.entry["workers"]
        with queue.locked():
            lease.entry.update(workers=workers, reservation=reservation.entry["ticket"],
                               admission={key: reservation.entry[key] for key in ("locks", "shared_locks", "workers")})
            lease.write()
        print(admission_notice(queue, queue.status(), name, reservation.entry), file=sys.stderr, flush=True)
        lease.acquire(reservation)
        env = bounded_environment(os.environ, workers)
        if "*" in reservation.entry["locks"]:
            # Cache eviction needs every other operation drained, which only exclusive admission ensures.
            from verification_cache import Maintenance
            Maintenance(queue, env).admitted()
        env.update(PW_TEST_EXECUTION_ROOT=str(queue.root), PW_TEST_EXECUTION_TICKET=lease.entry["ticket"])
        # The child retains admission and its reservation if its supervisor is interrupted.
        fd = fcntl.fcntl(lease.file.fileno(), fcntl.F_DUPFD, 200) if os.name != "nt" else None
        if fd is not None:
            env.update(PW_TEST_EXECUTION_FD=str(fd), PW_TEST_RESOURCE_FD=str(reservation.file.fileno()))
            with queue.locked():
                lease.entry["lease_fd"] = fd
                lease.write()
        process_options = {"pass_fds": (fd, reservation.file.fileno()), "start_new_session": True} if fd is not None else {}
        child = None
        try:
            print(f"test execution: {name} admitted (worker budget {workers})", file=sys.stderr, flush=True)
            started = time.time()
            child = subprocess.Popen(command, env=env, **process_options)
            with queue.locked():
                lease.entry["child_pid"] = child.pid
                lease.entry["child_identity"] = identity(child.pid)
                lease.write()
                reservation.entry.update(child_pid=child.pid, child_identity=lease.entry["child_identity"])
                reservation.write()
            while True:
                try:
                    code = child.wait(timeout=SAMPLE_SECONDS)
                except subprocess.TimeoutExpired:
                    report(queue, lease.entry["ticket"])
                    continue
                if code not in {130, 143}:
                    record_duration(queue, [name], time.time() - started)
                return code
        finally:
            if child is not None and child.poll() is None:
                if os.name == "nt":
                    subprocess.run(["taskkill", "/PID", str(child.pid), "/T", "/F"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
                else:
                    os.killpg(child.pid, signal.SIGTERM)
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    if os.name == "nt":
                        child.kill()
                    else:
                        os.killpg(child.pid, signal.SIGKILL)
                    child.wait()
            if fd is not None:
                os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["task", "run", "holding", "pause", "resume", "status", "cancel",
                                          "go-arguments", "plan", "batch-supervise", "batch-execute", "job-supervise"])
    parser.add_argument("--name", default="verification")
    parser.add_argument("--reason", default="Paused by request")
    arguments = sys.argv[1:]
    separator = arguments.index("--") if "--" in arguments else len(arguments)
    args = parser.parse_args(arguments[:separator])
    command = arguments[separator + 1:]
    if args.action == "go-arguments":
        env, bounded = bounded_go_arguments(command, os.environ)
        values = [env[name] for name in ("GO_TEST_P", "GO_TEST_PARALLEL", "GOMAXPROCS")]
        sys.stdout.buffer.write(("\0".join([*values, *bounded]) + "\0").encode())
        return 0
    task_request = None
    default_root = Path(tempfile.gettempdir()) / "test-execution" if os.name == "nt" else Path(f"/tmp/test-execution-{os.getuid()}")
    queue = Queue(os.environ.get("PW_TEST_EXECUTION_ROOT", default_root))
    if args.action == "task":
        binary, root, *arguments = command
        if arguments and arguments[0] in {"--background", "--wait"}:
            from verification_jobs import completion_command, submit, wait
            if queue.inherited():
                raise ValueError("verification job controls cannot run inside an admitted check")
            if arguments[0] == "--wait":
                if len(arguments) != 2:
                    raise ValueError("usage: ./task --wait <job-id>")
                return wait(root, arguments[1], lock_file)
            callback, background_arguments = completion_command(arguments[1:])
            if not scheduled_task(background_arguments):
                raise ValueError("--background requires a queued verification command")
            validate_task_selection(background_arguments)
            probe_task_request(binary, root, background_arguments)
            return submit(root, binary, background_arguments, str(Path(__file__).resolve()), callback)
        if not scheduled_task(arguments):
            os.execv(binary, [binary, "-d", root, *arguments])
        validate_task_selection(arguments)
        # A nested request inside an admitted check runs at once, so there is no wait to protect.
        if not queue.inherited():
            probe_task_request(binary, root, arguments)
        command = [binary, "-d", root, *serial_task_arguments(arguments)]
        args.name = ", ".join(task_names(arguments)) or "check-fast"
        task_request = (root, binary, arguments)
    if args.action == "job-supervise":
        from verification_jobs import supervise
        directory, binary, root, *arguments = command
        return supervise(directory, binary, root, arguments, str(Path(__file__).resolve()), lock_file)
    if args.action == "holding":
        return 0 if queue.inherited() else 1
    if args.action == "pause":
        queue.pause(args.reason)
        print("Test execution paused. Active work may finish; queued work will wait.")
        return 0
    if args.action == "resume":
        queue.resume()
        print("Test execution resumed.")
        return 0
    if args.action == "status":
        from verification_batch import status_details
        print(json.dumps(status_details(queue), indent=2))
        return 0
    if args.action == "cancel":
        from verification_cancel import cancel
        if queue.inherited():
            raise ValueError("cancellation cannot run inside an admitted check")
        return cancel(queue, command)
    if args.action == "batch-supervise":
        from verification_batch import supervise
        if os.name == "nt":
            raise ValueError("shared batches require POSIX lease inheritance")
        return supervise(queue, command[0], int(command[1]))
    if args.action == "batch-execute":
        from verification_execute import Executor
        if not queue.inherited():
            raise ValueError("batch execution requires active admission")
        return Executor(queue, command[0]).run()
    if args.action == "plan":
        separator = command.index("--") if "--" in command else len(command)
        if not queue.inherited():
            from verification_plan import invocation_profile
            return run(queue, "verification plan", [sys.executable, __file__, "plan", "--", *command],
                       invocation_profile(command[:separator], command[separator + 1:]))
        from verification_execute import direct
        return direct(command[:separator], command[separator + 1:])
    from verification_plan import catalog
    # A script that admits itself by name holds what the catalog declares for that name.
    spec = catalog()["resources"].get(args.name, EXCLUSIVE) if args.action == "run" else EXCLUSIVE
    if task_request and not queue.inherited():
        from verification_plan import execution_environment, invocation_profile, request
        from verification_batch import subscribe
        env = execution_environment({**os.environ, "PW_TEST_WORKERS": str(worker_budget(os.environ))})
        plan = request(task_request[2], env)
        if plan is not None and os.name != "nt":
            return subscribe(queue, Lease, lock_file, *task_request, plan, env)
        arguments = task_request[2]
        cli = arguments[arguments.index("--") + 1:] if "--" in arguments else []
        spec = invocation_profile(task_names(arguments), cli)
    if not command:
        parser.error("a command is required after --")
    return run(queue, args.name, command, spec)


def interrupted(signum, _frame):
    raise KeyboardInterrupt(signum)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
    except (ValueError, OSError) as error:
        print(f"test execution: {error}", file=sys.stderr)
        sys.exit(2)
