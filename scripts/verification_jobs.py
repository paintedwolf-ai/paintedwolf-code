"""Detached verification commands with a blocking completion handle."""

import json
import math
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
import uuid

from artifact_paths import artifact_root
from verification_plan import EXIT_UNVERIFIED, outcome_exit
from verification_state import read_json, write_json


def job_directory(root, job_id):
    if len(job_id) != 32 or any(c not in "0123456789abcdef" for c in job_id):
        raise ValueError("invalid verification job id")
    directory = artifact_root(root) / "jobs" / job_id
    if not (directory / "job.json").is_file():
        raise ValueError(f"unknown verification job: {job_id}")
    return directory


def completion_command(arguments):
    if not arguments or arguments[0] != "--on-complete":
        return None, arguments
    if len(arguments) < 3:
        raise ValueError("--on-complete requires a JSON argument array followed by a verification target")
    command = json.loads(arguments[1])
    if not isinstance(command, list) or not command or not all(isinstance(a, str) and a and "\0" not in a for a in command):
        raise ValueError("--on-complete requires a nonempty JSON array of command arguments")
    return command, arguments[2:]


def prune_jobs(jobs_dir, keep=100):
    if not jobs_dir.is_dir():
        return
    try:
        entries = sorted((p for p in jobs_dir.iterdir() if p.is_dir() and (p / "job.json").is_file()),
                         key=lambda p: (p / "job.json").stat().st_mtime, reverse=True)
        for old in entries[keep:]:
            if (old / "result.json").is_file():
                shutil.rmtree(old, ignore_errors=True)
    except OSError:
        pass


def submit(root, binary, arguments, script, callback=None):
    job_id = uuid.uuid4().hex
    jobs_dir = artifact_root(root) / "jobs"
    prune_jobs(jobs_dir)
    directory = jobs_dir / job_id
    directory.mkdir(parents=True, mode=0o700)
    write_json(directory / "job.json", {"job_id": job_id, "arguments": arguments,
                                       "submitted_at": time.time(), "on_complete": callback})
    # The worker acknowledges only after holding the completion lock.
    options = {"start_new_session": True} if os.name != "nt" else {
        "creationflags": subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.DETACHED_PROCESS}
    with (directory / "output.log").open("ab", buffering=0) as log:
        child = subprocess.Popen([sys.executable, script, "job-supervise", "--", str(directory),
                                  binary, str(Path(root).resolve()), *arguments],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=log, **options)
        ready = child.stdout.readline()
        child.stdout.close()
    if ready != b"ready\n":
        child.wait()
        raise ValueError(f"verification job failed to start; inspect {directory / 'output.log'}")
    print(json.dumps({"job_id": job_id, "pid": child.pid, "state": "submitted",
                      "wait": f"./task --wait {job_id}", "log": str(directory / "output.log")}))
    return 0


def supervise(directory, binary, root, arguments, script, lock_file):
    directory = Path(directory)
    with (directory / "completion.lock").open("a+") as completion:
        lock_file(completion)
        print("ready", flush=True)
        sys.stdout.close()
        child = None
        code = 2
        error = None
        with (directory / "output.log").open("ab", buffering=0) as log:
            try:
                # Retain the completion lock if the supervisor dies before its command.
                options = {"pass_fds": (completion.fileno(),)} if os.name != "nt" else {}
                child = subprocess.Popen([sys.executable, script, "task", "--", binary, root, *arguments],
                                         cwd=root, stdin=subprocess.DEVNULL, stdout=log, stderr=log, **options)
                code = child.wait()
            except KeyboardInterrupt:
                if child is not None:
                    child.send_signal(signal.SIGTERM)
                    child.wait()
                code = 130
            except OSError as failure:
                error = str(failure)
            result = {"job_id": directory.name, "state": "completed" if error is None else "unverified",
                      "exit_code": code if code >= 0 else 128 - code,
                      "finished_at": time.time(), "log": str(directory / "output.log")}
            if error:
                result["reason"] = error
            write_json(directory / "result.json", result)
    callback = read_json(directory / "job.json").get("on_complete")
    if callback:
        with (directory / "notification.log").open("ab", buffering=0) as log:
            try:
                delivered = subprocess.run([*callback, json.dumps(result)], cwd=root, stdin=subprocess.DEVNULL,
                                           stdout=log, stderr=log, timeout=30, check=False)
                notification = {"exit_code": delivered.returncode}
            except (OSError, subprocess.TimeoutExpired) as failure:
                notification = {"exit_code": 2, "reason": str(failure)}
            write_json(directory / "notification.json", notification)
    return 0


def unavailable_result(directory, reason):
    return {"job_id": directory.name, "state": "unverified", "exit_code": EXIT_UNVERIFIED,
            "reason": reason, "log": str(directory / "output.log")}


def completed_result(directory):
    try:
        result = read_json(directory / "result.json")
    except FileNotFoundError:
        return unavailable_result(directory, "Verification supervisor exited without a result")
    except (OSError, ValueError):
        return unavailable_result(directory, "Verification result could not be read")
    if not isinstance(result, dict):
        return unavailable_result(directory, "Verification result has an invalid shape")
    code, finished = result.get("exit_code"), result.get("finished_at")
    valid = (result.get("job_id") == directory.name and result.get("state") in ("completed", "unverified")
             and type(code) is int and 0 <= code <= 255
             and type(finished) in {int, float} and finished > 0
             and (type(finished) is int or math.isfinite(finished))
             and (result["state"] != "unverified" or code != 0)
             and result.get("log") == str(directory / "output.log"))
    return result if valid else unavailable_result(directory, "Verification result has an invalid shape")


def wait(root, job_id, lock_file):
    directory = job_directory(root, job_id)
    # The completion lock stays held until the command exits.
    try:
        with (directory / "completion.lock").open("r+") as completion:
            lock_file(completion)
            result = completed_result(directory)
    except OSError:
        result = unavailable_result(directory, "Verification completion lock could not be read")
    result = dict(result)
    # The caller reads the runner's verdict; the tool's own code stays visible.
    result["task_exit_code"] = result["exit_code"]
    result["exit_code"] = outcome_exit(result["task_exit_code"], result.get("state") == "completed")
    print(json.dumps(result))
    return result["exit_code"]
