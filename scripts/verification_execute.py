"""Execute shared stages on one source snapshot and issue request-specific outcomes."""

import json
import contextlib
from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import re
import subprocess
import time
import threading

import verification_reuse as reuse
from verification_batch import active_members, child_options, stop_child, write_lease
from verification_history import estimate, read_history, record_duration
from verification_plan import catalog, digest_command, expand, resolve_request, stage_key
from verification_resources import (Reservation, blocked_reason, bounded_environment, capacity, demand, entries,
                                    order_of, profile)
from verification_health import identity
from verification_cache import Maintenance
from verification_state import read_json, read_state, write_json

MAX_ADDED_PACKAGES = 16


def shared_peers(ready):
    first = ready[0]
    key = stage_key(first[1])
    packages = set(first[2])
    limit = len(packages) + MAX_ADDED_PACKAGES
    peers = [first]
    for candidate in ready[1:]:
        if stage_key(candidate[1]) != key:
            continue
        combined = packages.union(candidate[2])
        if first[1]["kind"] == "go" and len(combined) > limit:
            continue
        peers.append(candidate)
        packages = combined
    return peers


def json_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        yield value
        text = text[end:]


def list_arguments(stage):
    flags = []
    options = stage["options"]
    for index, option in enumerate(options):
        if option == "--tags":
            flags.append("-tags=" + options[index + 1])
        elif option == "--race":
            flags.append("-race")
    flags.extend(flag for flag in stage["flags"] if flag.partition("=")[0] in {"-tags", "-race"})
    return ["go", "list", "-json", *flags, *stage["packages"]]


def flag_tags(options, flags):
    """Build tags named by a Go recipe's options and flags."""
    tags = set()
    for index, option in enumerate(options):
        if option == "--tags" and index + 1 < len(options):
            tags.update(t for t in re.split(r"[\s,]+", options[index + 1]) if t)
    for flag in flags:
        name, _, value = flag.partition("=")
        if name == "-tags":
            tags.update(t for t in re.split(r"[\s,]+", value) if t)
    return tags


def tier_tags(data):
    """Tags some declared Go target enables; each gates a test tier."""
    tiers = {}
    for name, recipe in data["go"].items():
        for tag in flag_tags(recipe["options"], recipe.get("flags", [])):
            tiers.setdefault(tag, []).append(name)
    return tiers


BUILD_CONSTRAINT = re.compile(r"^//go:build\s+(.+)$")
IDENTIFIER = re.compile(r"[A-Za-z_][A-Za-z0-9_.]*")


def constraint_tags(path):
    """Identifiers in a file's //go:build line, read from the header before the package clause."""
    try:
        with open(path, encoding="utf-8", errors="replace") as stream:
            for line in stream:
                match = BUILD_CONSTRAINT.match(line.strip())
                if match:
                    return set(IDENTIFIER.findall(match.group(1)))
                if line.startswith("package "):
                    break
    except OSError:
        pass
    return set()


def excluded_tier_tests(records, tiers, enabled):
    """Per package, the test files a tier tag keeps out of a stage that does not enable it."""
    out = {}
    for record in records:
        files, tags = [], set()
        for name in record.get("IgnoredGoFiles") or []:
            if not name.endswith("_test.go"):
                continue
            missing = (constraint_tags(Path(record.get("Dir", "")) / name) & set(tiers)) - enabled
            if missing:
                files.append(name)
                tags |= missing
        if files:
            out[record["ImportPath"]] = {"tags": sorted(tags), "files": sorted(files)}
    return out


def exclusion_notice(name, excluded, tiers):
    """One line naming the tier tests a stage left out and the targets that run them."""
    files = sum(len(entry["files"]) for entry in excluded.values())
    tags = sorted({tag for entry in excluded.values() for tag in entry["tags"]})
    targets = sorted({target for tag in tags for target in tiers.get(tag, [])})
    return (f"test execution: {name}: {files} test files in {len(excluded)} packages need build tags "
            f"{', '.join(tags)} and did not run; {', '.join(targets)} run them")


def task_command(stage):
    # Fresh receipts require execution even when snapshot freshness records exist.
    return ["./task", "--force", *stage["arguments"]]


def direct(names, cli):
    """Execute the catalog for nested or exclusive invocations, honouring the same selection."""
    for stage in expand(names, cli):
        if stage["kind"] == "task":
            command = task_command(stage)
        else:
            command = digest_command(stage, stage["packages"])
            if stage["exclude"]:
                index = command.index("--name")
                command[index:index] = ["--exclude-pkg", stage["exclude"]]
        code = run_direct(command)
        if code:
            return code
    return 0


def run_direct(command):
    child = subprocess.Popen(command, **child_options())
    try:
        return child.wait()
    finally:
        stop_child(child)


class StageEvents:
    """Package failures a running Go stage reports before its digest completes."""

    def __init__(self, events, raw):
        self.paths = {"events": Path(events), "raw": Path(raw)}
        self.offsets = {"events": 0, "raw": 0}
        self.partial = {"events": b"", "raw": b""}

    def lines(self, source):
        try:
            with self.paths[source].open("rb") as stream:
                stream.seek(self.offsets[source])
                data = stream.read()
        except FileNotFoundError:
            return []
        self.offsets[source] += len(data)
        complete, _, self.partial[source] = (self.partial[source] + data).rpartition(b"\n")
        values = []
        for line in complete.splitlines():
            try:
                value = json.loads(line)
            except ValueError:
                continue
            if isinstance(value, dict):
                values.append(value)
        return values

    def failures(self):
        """A test binary's failing exit, a terminal package failure, or a build failure establishes that the
        package failed; a signal is an interruption, not a result."""
        found = {}
        for event in self.lines("events"):
            if isinstance(event.get("package"), str) and type(event.get("exit_code")) is int and event["exit_code"] > 0:
                found[event["package"]] = {key: event[key] for key in ("output", "tests") if key in event}
        for event in self.lines("raw"):
            if event.get("Action") == "fail" and isinstance(event.get("Package"), str) and not event.get("Test"):
                found.setdefault(event["Package"], {})
            elif event.get("Action") == "build-fail" and isinstance(event.get("ImportPath"), str):
                found.setdefault(event["ImportPath"].split(" [", 1)[0], {"build_failed": True})
        return found


class Executor:
    def __init__(self, queue, directory):
        self.queue = queue
        self.directory = Path(directory)
        self.plan = read_json(self.directory / "plan.json")
        self.requests = self.plan["requests"]
        self.members = self.plan["batch"]["members"]
        self.source = os.environ["PW_SOURCE_SNAPSHOT_COMMIT"]
        self.tree = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], text=True).strip()
        self.completed = {r["ticket"]: set() for r in self.requests}
        self.running = {r["ticket"]: set() for r in self.requests}
        self.evidence = {r["ticket"]: {} for r in self.requests}
        self.finished = set()
        self.cache = {}
        self.selections = {}
        self.exclusions = {}
        self.stages = []
        self.started = time.time()
        self.stopping = threading.Event()
        self.sequence = 0
        self.progress = {}
        self.catalog = catalog()
        self.history = read_history(queue)
        self.cache_maintenance = Maintenance(queue, dict(os.environ))
        self.store = reuse.default_store()
        # Every member shares one declared environment; the requester recorded its identity.
        self.environment_identity = self.requests[0].get("environment_identity", "")

    def arrival(self, consumers):
        return min(order_of(request) for request in self.requests if request["ticket"] in consumers)

    def interested(self, consumers):
        return (not self.stopping.is_set() and any(c not in self.finished for c in consumers)
                and bool(active_members(self.queue, consumers)))

    def run_command(self, command, log, consumers, env=None, cwd=None, errors=None, reservation=None):
        with contextlib.ExitStack() as files:
            if reservation is None:
                reservation = files.enter_context(Reservation(
                    self.queue, self.queue.lock_file, self.plan["batch"]["ticket"], log.stem,
                    profile({"kind": "go"}, self.catalog), self.plan["batch"]["workers"], self.arrival(consumers)))
            interested = lambda: self.interested(consumers)
            if not reservation.acquire(interested):
                log.touch()
                if errors:
                    errors.touch()
                return 130
            env = bounded_environment(env or os.environ, reservation.entry["workers"])
            env["PW_TEST_RESOURCE_FD"] = str(reservation.file.fileno())
            options = child_options()
            options["pass_fds"] = (*options.get("pass_fds", ()), reservation.file.fileno())
            output = files.enter_context(log.open("wb"))
            error_output = files.enter_context(errors.open("wb")) if errors else subprocess.STDOUT
            child = None
            try:
                with self.queue.locked():
                    child = subprocess.Popen(command, stdout=output, stderr=error_output,
                                             cwd=cwd, env=env, **options)
                    reservation.entry["child_pid"] = child.pid
                    reservation.entry["child_identity"] = identity(child.pid)
                    reservation.entry["activity_paths"] = [str(path) for path in
                        (log, errors, env.get("PW_TEST_STAGE_RAW"), env.get("PW_TEST_STAGE_RESULT"),
                         env.get("PW_TEST_STAGE_EVENTS")) if path]
                    reservation.write()
                    self.operation_pid(log.stem, child.pid)
                while child.poll() is None:
                    if not interested():
                        stop_child(child)
                        return 130
                    time.sleep(0.1)
                return child.wait()
            finally:
                if child is not None:
                    stop_child(child)
                    with self.queue.locked():
                        self.operation_pid(log.stem, None)

    def operation_pid(self, name, pid):
        path = self.queue.root / (self.plan["batch"]["ticket"] + ".lease")
        with path.open("r+") as lease:
            entry = json.load(lease)
            pids = entry.setdefault("operation_pids", {})
            if pid is None:
                pids.pop(name, None)
            else:
                pids[name] = pid
            write_lease(lease, entry)

    def packages(self, stage, request_id):
        key = json.dumps(stage, sort_keys=True)
        if key not in self.selections:
            log = self.directory / f"selection-{len(self.selections)}.json"
            errors = log.with_suffix(".log")
            code = self.run_command(list_arguments(stage), log, [request_id], cwd="lycaon", errors=errors)
            text = log.read_text(errors="replace")
            if code:
                raise ValueError(f"Package selection failed (exit {code}); {errors}\n{errors.read_text(errors='replace')}")
            try:
                records = list(json_stream(text))
                errors = [record for record in records if record.get("Error") or record.get("DepsErrors")]
                if errors:
                    raise ValueError(json.dumps(errors))
                packages = sorted({record["ImportPath"] for record in records
                                   if not stage["exclude"] or not re.search(stage["exclude"], record["ImportPath"])})
            except (KeyError, ValueError, TypeError) as error:
                raise ValueError(f"Invalid package selection; {log}: {error}") from error
            if not packages:
                raise ValueError(f"Package selection matched no packages; {log}")
            self.selections[key] = packages
            excluded = excluded_tier_tests(records, tier_tags(self.catalog),
                                           flag_tags(stage["options"], stage["flags"]))
            self.exclusions[key] = {p: entry for p, entry in excluded.items() if p in packages}
        return self.selections[key]

    def ordered_evidence(self, ticket):
        return [self.evidence[ticket][index] for index in sorted(self.evidence[ticket])]

    def receipt(self, request, status, code, reason=None):
        ticket = request["ticket"]
        value = {"request": ticket, "names": request["plan"]["names"], "batch_id": self.directory.name,
                 "source_root": request["source"], "source_commit": self.source, "source_tree": self.tree,
                 "status": status, "exit_code": code, "queued_at": request["queued_at"],
                 "started_at": self.started, "finished_at": time.time(), "compatibility": request["compatibility"],
                 "evidence": self.ordered_evidence(ticket),
                 "unverified_stages": [stage["name"] for index, stage in enumerate(request["plan"]["stages"])
                                       if index not in self.completed[ticket] and index not in self.evidence[ticket]]}
        if reason:
            value["reason"] = reason
        write_json(self.directory / (ticket + ".json"), value)
        self.finished.add(ticket)
        print(f"test execution: {', '.join(value['names'])}: {status}; receipt {self.directory / (ticket + '.json')}", flush=True)
        tiers = tier_tags(self.catalog)
        for evidence in value["evidence"]:
            if evidence.get("excluded_tests"):
                print(exclusion_notice(evidence["stage"], evidence["excluded_tests"], tiers), flush=True)

    def settle(self, request, index, stage, packages, results, failed):
        ticket = request["ticket"]
        self.evidence[ticket][index] = {"stage": stage["name"], "packages": packages if stage["kind"] == "go" else [],
                                        "results": results}
        excluded = self.exclusions.get(json.dumps(stage, sort_keys=True))
        if excluded:
            self.evidence[ticket][index]["excluded_tests"] = excluded
        if not failed:
            self.completed[ticket].add(index)
        write_json(self.directory / (ticket + ".progress.json"),
                   {"completed": sorted(self.completed[ticket]), "evidence": self.ordered_evidence(ticket)})

    def ready(self):
        """Every stage of every request that is neither settled nor running. A gate's stages are
        independent; resource declarations decide what may overlap."""
        live = active_members(self.queue, self.members)
        ready = []
        for request in self.requests:
            ticket = request["ticket"]
            if ticket in self.finished:
                continue
            if ticket not in live:
                record = read_state(self.directory / (ticket + ".cancelled.json"))
                self.receipt(request, "cancelled", 130, record.get("reason"))
                continue
            waiting = []
            for index, stage in enumerate(request["plan"]["stages"]):
                if index in self.completed[ticket] or index in self.running[ticket]:
                    continue
                try:
                    packages = self.packages(stage, ticket) if stage["kind"] == "go" else ["task"]
                except ValueError as error:
                    self.receipt(request, "unverified", 2, str(error))
                    break
                cached = self.cache.get(stage_key(stage), {})
                results = {p: cached[p] for p in packages if p in cached}
                failed = next((r for r in results.values() if r["status"] != "passed"), None)
                if failed or len(results) == len(packages):
                    self.settle(request, index, stage, packages, results, failed)
                    if failed:
                        self.receipt(request, failed["status"], failed["exit_code"])
                        break
                    continue
                waiting.append((request, stage, [p for p in packages if p not in cached], index))
            else:
                if not waiting and not self.running[ticket]:
                    self.receipt(request, "passed", 0)
                ready.extend(waiting)
        return ready

    def priority(self, item):
        """Longest expected stage first; an unmeasured stage may be the longest, so it leads."""
        request, stage, _, index = item
        expected = estimate(self.history, stage["name"])
        return (-(float("inf") if expected is None else expected), self.requests.index(request), index)

    def candidate(self, request, stage):
        spec = profile(stage, self.catalog)
        return {"ticket": "~", "name": stage["name"], "state": "queued", "locks": spec["locks"],
                "shared_locks": spec.get("shared_locks", []), "estimate": estimate(self.history, stage["name"]),
                "workers": demand(spec, self.plan["batch"]["workers"]), "order": order_of(request)}

    def ordered_packages(self, stage, packages):
        """Longest packages start first, so the package that finishes last is not also the longest."""
        durations = reuse.package_durations(self.queue, stage_key(stage))
        return sorted(packages, key=lambda p: (-durations.get(p, float("inf")), p))

    def start_stage(self, peers, stage_id, pool):
        _, first, _, _ = peers[0]
        key = stage_key(first)
        packages = sorted({p for _, _, ps, _ in peers for p in ps})
        consumers = [r["ticket"] for r, _, _, _ in peers]
        log = self.directory / (stage_id + ".log")
        result_path = self.directory / (stage_id + ".json")
        raw = self.directory / (stage_id + "-go.json")
        events = self.directory / (stage_id + "-events.jsonl")
        command = task_command(first) if first["kind"] == "task" else digest_command(
            first, self.ordered_packages(first, packages))
        env = {k: v for k, v in os.environ.items() if k not in {"PW_TEST_STAGE_RESULT", "PW_TEST_STAGE_RAW"}}
        artifacts = None
        if first["name"] == "den:harness:test":
            artifacts = str(self.directory / (stage_id + "-browser"))
            env.setdefault("LYCAON_E2E_OUTPUT_DIR", artifacts)
            artifacts = env["LYCAON_E2E_OUTPUT_DIR"]
        if first["kind"] == "go":
            env.update(PW_TEST_STAGE_RESULT=str(result_path), PW_TEST_STAGE_RAW=str(raw),
                       PW_TEST_STAGE_EVENTS=str(events), PW_TEST_STAGE_ID=stage_id,
                       PW_TEST_BATCH_DIRECTORY=str(self.directory),
                       PW_TEST_ENVIRONMENT_IDENTITY=self.environment_identity)
            if self.store is not None:
                env["PW_TEST_REUSE_STORE"] = str(self.store)
            env.setdefault("GO_TEST_P", str(len(packages)))
            if "--race" in first["options"] or any(flag.partition("=")[0] == "-race" for flag in first["flags"]):
                env.setdefault("GOMAXPROCS", "2")
        names = sorted({s["name"] for _, s, _, _ in peers})
        print(f"test execution: {stage_id}: {', '.join(names)} ({len(consumers)} requests); log {log}", flush=True)
        reservation = Reservation(self.queue, self.queue.lock_file, self.plan["batch"]["ticket"],
                                  ", ".join(names), profile(first, self.catalog), self.plan["batch"]["workers"],
                                  self.arrival(consumers))
        reservation.__enter__()
        try:
            future = pool.submit(self.run_command, command, log, consumers, env=env, reservation=reservation)
        except BaseException:
            reservation.__exit__(None, None, None)
            raise
        for request, _, _, index in peers:
            self.running[request["ticket"]].add(index)
        result = {"stage_id": stage_id, "names": names, "command": command, "consumers": consumers,
                  "started_at": time.time(), "log": str(log), "workers": reservation.entry["workers"],
                  "locks": reservation.entry["locks"], "shared_locks": reservation.entry["shared_locks"]}
        if artifacts is not None:
            result["artifacts"] = artifacts
        job = {"future": future, "first": first, "key": key, "packages": packages, "reservation": reservation,
               "result_path": result_path, "result": result, "events_path": events,
               "needs": [(request, index, stage, needed) for request, stage, needed, index in peers]}
        if first["kind"] == "go":
            job["events"] = StageEvents(events, raw)
        return job

    def early_failures(self, job):
        """A request whose package already failed has its answer and stops waiting on the rest of the stage;
        the stage continues only for requests still interested in it."""
        if "events" not in job:
            return
        failures = job["events"].failures()
        if not failures:
            return
        stage_id = job["result"]["stage_id"]
        for request, index, stage, needed in job["needs"]:
            ticket = request["ticket"]
            failed = [p for p in needed if p in failures]
            if ticket in self.finished or not failed:
                continue
            results = {p: {"status": "failed", "exit_code": 1, "stage_id": stage_id, "log": job["result"]["log"],
                           "raw": str(job["events"].paths["raw"]), **failures[p]} for p in failed}
            self.running[ticket].discard(index)
            self.settle(request, index, stage, self.packages(stage, ticket), results, True)
            self.receipt(request, "failed", 1,
                         f"{', '.join(failed)} failed in {stage_id} before the stage finished")

    def finish_stage(self, job):
        """Results publish before the stage's reservation releases, so work waiting on its resources sees
        which requests no longer need it."""
        try:
            self.publish_stage(job)
        finally:
            job["reservation"].__exit__(None, None, None)

    def publish_stage(self, job):
        first, key, packages = job["first"], job["key"], job["packages"]
        result, result_path = job["result"], job["result_path"]
        consumers, stage_id, log = result["consumers"], result["stage_id"], Path(result["log"])
        code = job["future"].result()
        result.update(exit_code=code, finished_at=time.time())
        for request, index, _, _ in job["needs"]:
            self.running[request["ticket"]].discard(index)
        print(log.read_text(errors="replace"), end="", flush=True)
        if code not in {130, 143}:
            record_duration(self.queue, result["names"], result["finished_at"] - result["started_at"])
        if code == 130 and all(c in self.finished for c in consumers):
            self.stages.append(result)
            write_json(self.directory / "stages.json", self.stages)
            return
        cache = self.cache.setdefault(key, {})
        if first["kind"] == "task":
            status = "passed" if code == 0 else ("unverified" if code < 0 or code in {130, 143} else "failed")
            cache["task"] = {"status": status, "exit_code": 2 if status == "unverified" else code,
                             "stage_id": stage_id, "log": str(log)}
            if "artifacts" in result:
                cache["task"]["artifacts"] = result["artifacts"]
                print(f"test execution: browser artifacts: {result['artifacts']}", flush=True)
        else:
            report = read_json(result_path) if result_path.exists() else {}
            expected_code = 0 if report.get("process_exit_code") == report.get("digest_exit_code") == 0 else 1
            valid = report.get("completed", False) and code == expected_code
            outcomes = reuse.read_events(job["events_path"])
            for package in packages:
                action = report.get("packages", {}).get(package)
                status = "unverified" if not valid or action not in {"pass", "skip", "fail"} else (
                    "failed" if action == "fail" else "passed")
                cache[package] = {"status": status, "exit_code": {"passed": 0, "failed": 1, "unverified": 2}[status],
                                  "stage_id": stage_id, "log": str(log), "report": str(result_path)}
                if status == "passed" and outcomes.get(package, {}).get("reused"):
                    cache[package]["reused"] = outcomes[package]["reused"]
            reused = sum(1 for p in packages if "reused" in cache[p])
            if reused:
                print(f"test execution: {stage_id}: {reused} of {len(packages)} packages reused passing results "
                      "recorded for identical builds and inputs", flush=True)
            reuse.record_package_durations(self.queue, key, outcomes)
            result["packages"] = packages
            result["reused_packages"] = reused
        self.history = read_history(self.queue)
        self.stages.append(result)
        write_json(self.directory / "stages.json", self.stages)

    def run(self):
        subprocess.run(["git", "update-ref", "refs/verification/batches/" + self.directory.name, self.source], check=True)
        # The stamp is taken in the checkout, not the snapshot, so later edits there are observable.
        from verification_advisories import source_stamp
        write_json(self.directory / "source.json", {"source_commit": self.source, "source_tree": self.tree,
                   "source_stamp": source_stamp(self.plan["batch"]["source"])})
        with self.queue.locked():
            path = self.queue.root / (self.plan["batch"]["ticket"] + ".lease")
            with path.open("r+") as lease:
                entry = json.load(lease)
                entry["phase"] = "execution"
                write_lease(lease, entry)
        try:
            for request in self.requests:
                try:
                    request["plan"]["stages"] = resolve_request(request)
                except (OSError, ValueError) as error:
                    self.receipt(request, "unverified", 2, f"Cannot resolve the request on the captured source: {error}")
            write_json(self.directory / "resolved-plan.json", self.requests)
            self.run_stages()
        finally:
            for request in self.requests:
                if request["ticket"] not in self.finished:
                    self.receipt(request, "unverified", 2, "Batch execution interrupted")
            write_json(self.directory / "finished.json", {"finished_at": time.time()})
        write_json(self.directory / "summary.json", {"requests": len(self.requests), "executions": len(self.stages),
                   "requested_stages": sum(len(r["plan"]["stages"]) for r in self.requests),
                   "reused_packages": sum(stage.get("reused_packages", 0) for stage in self.stages),
                   "source_commit": self.source, "source_tree": self.tree,
                   "started_at": self.started, "finished_at": time.time()})
        if self.store is not None:
            reuse.prune(self.store)
        return 0 if all(read_json(self.directory / (r["ticket"] + ".json"))["exit_code"] == 0 for r in self.requests) else 1

    def next_stages(self, ready, pending):
        """Stages to start now, longest first. A batch keeps at most one reservation waiting, so one large
        gate cannot queue ahead of every other batch; while it waits, stages admissible at once still start."""
        ready = sorted((item for item in ready if stage_key(item[1]) not in pending), key=self.priority)
        if not ready:
            return []
        waiting = any(job["reservation"].entry["state"] == "queued" for job in pending.values())
        with self.queue.locked():
            records = entries(self.queue, self.queue.lock_file)
        chosen, taken = [], set()
        for item in ready:
            key = stage_key(item[1])
            if key in taken:
                continue
            exclusive = "*" in profile(item[1], self.catalog)["locks"]
            if exclusive and (pending or chosen):
                break
            candidate = self.candidate(item[0], item[1])
            admissible = blocked_reason(candidate, records) is None
            if waiting and not admissible:
                continue
            chosen.append(shared_peers([item, *(other for other in ready if other is not item)]))
            taken.add(key)
            if exclusive or len(pending) + len(chosen) >= capacity():
                break
            if admissible:
                # Later candidates in this pass see the capacity and locks this stage will hold.
                records = [*records, {**candidate, "ticket": f"~{key}", "state": "running",
                                      "started_at": time.time()}]
            else:
                waiting = True
        return chosen

    def run_stages(self):
        pending = {}
        pool = ThreadPoolExecutor(max_workers=capacity())
        # Off the scheduler thread, which alone reaps stages, so a trim never delays reaping.
        trimmer = ThreadPoolExecutor(max_workers=1)
        trimming = []
        def maintain():
            if not trimming or trimming[0].done():
                trimming[:] = [trimmer.submit(self.cache_maintenance.scheduled, self.plan["batch"]["ticket"])]
        try:
            maintain()
            # Early package discovery keeps asynchronous stage dispatch from blocking.
            for request in self.requests:
                for stage in request["plan"]["stages"]:
                    if stage["kind"] == "go" and request["ticket"] not in self.finished:
                        try:
                            self.packages(stage, request["ticket"])
                        except ValueError as error:
                            self.receipt(request, "unverified", 2, str(error))
            while len(self.finished) < len(self.requests) or pending:
                completed = False
                for key, job in list(pending.items()):
                    if job["future"].done():
                        self.finish_stage(job)
                        del pending[key]
                        self.progress.pop(job["result"]["stage_id"])
                        completed = True
                    else:
                        self.early_failures(job)
                if completed:
                    maintain()
                for peers in self.next_stages(self.ready(), pending):
                    self.sequence += 1
                    stage_id = f"stage-{self.sequence:03d}"
                    self.progress[stage_id] = {"names": sorted({s["name"] for _, s, _, _ in peers}),
                                               "consumers": [r["ticket"] for r, _, _, _ in peers]}
                    job = self.start_stage(peers, stage_id, pool)
                    pending[job["key"]] = job
                write_json(self.directory / "progress.json", {"stages": self.progress,
                    "source_commit": self.source, "source_tree": self.tree, "executor_pid": os.getpid()})
                if pending:
                    time.sleep(0.1)
        finally:
            self.stopping.set()
            pool.shutdown(wait=True)
            trimmer.shutdown(wait=True)
