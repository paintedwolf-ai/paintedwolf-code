"""Hosted lane execution, resource evidence, and queue cleanup."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time

from artifact_paths import artifact_root
from ci_policy.evidence import oom_events, classify
from ci_policy import hosted_plan, timings, verification_report

ROOT = Path(__file__).resolve().parents[2]


def task_json(arguments):
    """Run a queue control through ./task and parse the JSON it prints after Task's command echo."""
    result = subprocess.run(["./task", *arguments], cwd=ROOT, capture_output=True, text=True)
    output = result.stdout[result.stdout.find("{"):] if "{" in result.stdout else ""
    return result.returncode, json.loads(output) if output else None


def release_leftover_requests(step):
    """A hosted runner serves one job; withdraw its abandoned queue requests."""
    if os.environ.get("GITHUB_ACTIONS") != "true":
        raise ValueError("release withdraws every queued request, which is only this job's own on a hosted runner")
    _, status = task_json(["test:status"])
    requests = [entry for entry in status["runs"] if entry.get("kind") == "request"]
    unreleased = []
    for entry in requests:
        code, value = task_json(["test:cancel", "--", entry["ticket"], "--force",
                                 "--reason", f"{entry['name']} outlived the {step} step"])
        print(f"cancelled {entry['name']} ({entry['ticket']}): {json.dumps(value, sort_keys=True)}", flush=True)
        if code != 0:
            unreleased.append(entry["ticket"])
    return unreleased


def resource_line():
    """Memory, disk, and the largest processes, for diagnosing a runner that disappears mid-lane."""
    meminfo = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
    mib = {key: int(meminfo[key].split()[0]) // 1024 for key in ("MemAvailable", "SwapFree")}
    disks = {where: shutil.disk_usage(where).free // 2**30 for where in sorted({"/", tempfile.gettempdir()})}
    processes = []
    for status in Path("/proc").glob("[0-9]*/status"):
        try:
            fields = dict(line.split(":", 1) for line in status.read_text().splitlines() if ":" in line)
        except OSError:
            continue
        if "VmRSS" in fields:
            processes.append((int(fields["VmRSS"].split()[0]) // 1024, fields["Name"].strip(), status.parent.name))
    top = ", ".join(f"{name}[{pid}] {rss} MiB" for rss, name, pid in sorted(processes, reverse=True)[:4])
    free = ", ".join(f"{where} {gib} GiB" for where, gib in disks.items())
    return (f"runner resources: available {mib['MemAvailable']} MiB, swap free {mib['SwapFree']} MiB; "
            f"disk free {free}; largest {top}")


def sample_resources(stop, interval=60):
    while not stop.wait(interval):
        print(resource_line(), flush=True)


HISTORY_KEYS = ("failed_targets", "passed_targets", "native_failed", "native_passed", "contract_failures")


def parse_early_history(raw, targets):
    """Parse PW_CI_EARLY_TARGET as a validated JSON history dict or a single target name."""
    if not raw:
        return {}
    if raw.startswith("{"):
        try:
            data = json.loads(raw)
        except ValueError:
            return {}
        if not isinstance(data, dict):
            return {}
        return {key: [item for item in value if isinstance(item, str)]
                for key, value in data.items() if key in HISTORY_KEYS and isinstance(value, list)}
    return {"failed_targets": [raw]} if raw in targets else {}


def run_lane(name, shard="", merge_group=False):
    lane = hosted_plan.lanes()[name]
    count = lane.get("shards", 1)
    if (shard == "") != (count == 1) or shard and shard not in {f"{k}/{count}" for k in range(1, count + 1)}:
        raise ValueError(f"CI lane {name} has {count} shard(s); got {shard!r}")
    targets = lane["targets"]
    scope = None
    if os.environ.get("PW_CI_AFFECTED") == "1" or "budgets" in targets:
        from ci_policy.impact import change
        scope = change()
    directory = artifact_root(ROOT) / "ci"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "run.json"
    record = {"lane": name, "shard": shard, "targets": targets, "started_at": time.time(), "status": "running", "scope": scope}
    record['identity'] = {'run': os.environ.get('GITHUB_RUN_ID', ''),
                          'attempt': os.environ.get('GITHUB_RUN_ATTEMPT', ''),
                          'sha': os.environ.get('GITHUB_SHA', '')}
    try:
        record['job_started_at'] = float(os.environ['PW_CI_JOB_STARTED_AT'])
    except (KeyError, ValueError):
        record['job_started_at'] = None
    record["oom_before"] = oom_events()
    path.write_text(json.dumps(record, indent=2) + "\n")
    # The hosted lane budget bounds Go runs.
    environment = {"PW_GO_TEST_TIMEOUT_SECONDS": str(lane["minutes"] * 60), **os.environ}
    if scope and scope["base"]:
        environment["PW_CHANGE_BASE"] = scope["base"]
    # Memory-heavy lanes cap process parallelism.
    if "workers" in lane:
        environment["PW_TEST_WORKERS"] = str(lane["workers"])
    # Shards use the plan's shared package weights.
    measured = environment.pop("PW_CI_PACKAGE_TIMINGS", "")
    if shard:
        environment["PW_GO_SHARD"] = shard
        if measured:
            weights = directory / "package-weights.json"
            weights.write_text(measured)
            environment["PW_GO_TIMINGS"] = str(weights)
    # Resource samples survive runner loss in the live log.
    stop = threading.Event()
    if os.environ.get("PW_TEST_HOST") == "dedicated" and Path("/proc/meminfo").exists():
        threading.Thread(target=sample_resources, args=(stop,), daemon=True).start()
    command = ["./task", *hosted_plan.invocation(targets)]
    if "arguments" in lane:
        command += ["--", *lane["arguments"]]

    try:
        code = 0
        record["phases"] = []
        packages = None
        if scope and name == "behavior":
            from ci_policy.impact import go_scope
            packages = go_scope(scope)
            if packages:
                command += ["--", *packages]
                record["packages"] = packages
        early_raw = environment.pop('PW_CI_EARLY_TARGET', '')
        history = parse_early_history(early_raw, targets) if (name == 'ready' and os.environ.get('GITHUB_ACTIONS') == 'true') else {}
        if lane.get("preflight"):
            from ci_policy.merge_preflight import preflight_commands
            preflights = preflight_commands(lane["preflight"], history)
            record["preflight"] = {"command": preflights[0], "started_at": time.time()}
            if len(preflights) > 1:
                record["preflight"]["phases"] = []
            path.write_text(json.dumps(record, indent=2) + "\n")
            for preflight_cmd in preflights:
                if code != 0:
                    break
                record["preflight"]["command"] = preflight_cmd
                phase = {"command": preflight_cmd, "started_at": time.time()}
                if "phases" in record["preflight"]:
                    record["preflight"]["phases"].append(phase)
                path.write_text(json.dumps(record, indent=2) + "\n")
                code = subprocess.call(preflight_cmd, cwd=ROOT, env=environment)
                phase.update(exit_code=code, finished_at=time.time())
            record["preflight"].update(exit_code=code, finished_at=time.time())
        commands = [command]
        if name == 'ready':
            from ci_policy.native_admission import commands as native_commands
            from ci_policy.merge_preflight import ready_commands
            commands = ready_commands(targets, native_commands(scope), history)
        if code == 0 and merge_group:
            from ci_policy.merge_preflight import commands as ordered_commands
            commands = ordered_commands(lane, hosted_plan.invocation(targets), packages)
        for command in commands:
            if code != 0:
                break
            phase = {"command": command, "started_at": time.time()}
            record["phases"].append(phase)
            path.write_text(json.dumps(record, indent=2) + "\n")
            code = subprocess.call(command, cwd=ROOT, env=environment)
            phase.update(exit_code=code, finished_at=time.time())
    except (subprocess.SubprocessError, OSError, ValueError) as error:
        code = 2
        record["execution_error"] = {"type": type(error).__name__, "message": str(error)}
        if isinstance(error, subprocess.CalledProcessError):
            record["execution_error"].update(command=error.cmd, exit_code=error.returncode, stderr=error.stderr)
    finally:
        stop.set()
    record["oom_after"] = oom_events()
    record.update(finished_at=time.time(), exit_code=code,
                  status="passed" if code == 0 else "failed" if code == 1 else "unverified")
    record["classification"] = classify(record, verification_report.failures(artifact_root(ROOT)))
    path.write_text(json.dumps(record, indent=2) + "\n")
    timings.record(artifact_root(ROOT), record)
    return code
