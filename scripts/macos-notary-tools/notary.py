#!/usr/bin/env python3
"""Staple and validate tickets before Tauri packages the signed application."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import uuid


def notarize(arguments, directory, execute=subprocess.run):
    result = execute(["/usr/bin/xcrun", *arguments], capture_output=True, text=True)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode != 0:
        return result.returncode
    try:
        submission = json.loads(result.stdout)
        identifier = str(uuid.UUID(submission["id"]))
    except (ValueError, KeyError, TypeError):
        return result.returncode
    # Ask Apple which exact code hashes it accepted, without recording credentials.
    credentials = []
    fields = {"--key", "--key-id", "--issuer", "--apple-id", "--password",
              "--team-id", "--keychain-profile", "--keychain"}
    for index, argument in enumerate(arguments[:-1]):
        if argument in fields:
            credentials.extend((argument, arguments[index + 1]))
    directory.mkdir(parents=True, exist_ok=True)
    evidence = {"submission_id": identifier, "status": submission.get("status")}
    try:
        log = execute(["/usr/bin/xcrun", "notarytool", "log", identifier, *credentials],
                      capture_output=True, text=True, timeout=120)
        evidence["log_exit_code"] = log.returncode
        if log.returncode == 0:
            evidence["log"] = json.loads(log.stdout)
    except (OSError, ValueError, subprocess.TimeoutExpired):
        evidence["log_unavailable"] = True
    (directory / f"notary-{identifier}.json").write_text(json.dumps(evidence, indent=2) + "\n")
    return result.returncode


def staple(arguments, directory, execute=subprocess.run, sleep=time.sleep):
    if arguments[:2] != ["stapler", "staple"] or len(arguments) < 3:
        raise ValueError("expected a stapler staple invocation")
    directory.mkdir(parents=True, exist_ok=True)
    descriptor, name = tempfile.mkstemp(prefix="staple-", suffix=".json", dir=directory)
    receipt = Path(name)
    # An interrupted native operation must not leave a successful build receipt.
    with os.fdopen(descriptor, "w") as stream:
        json.dump({"success": False, "arguments": arguments, "attempts": []}, stream)
    evidence = {"success": False, "arguments": arguments, "attempts": []}
    for attempt, delay in enumerate((0, 10, 30), start=1):
        if delay:
            sleep(delay)
        commands = []
        for command in (arguments, ["stapler", "validate", *arguments[2:]]):
            try:
                result = execute(["/usr/bin/xcrun", *command], capture_output=True, text=True, timeout=120)
                record = {"command": command, "exit_code": result.returncode,
                          "stdout": result.stdout, "stderr": result.stderr}
            except (OSError, subprocess.TimeoutExpired) as error:
                record = {"command": command, "exit_code": None, "error": str(error)}
            commands.append(record)
            if record["exit_code"] != 0:
                break
        evidence["attempts"].append({"attempt": attempt, "commands": commands})
        evidence["success"] = len(commands) == 2 and all(row["exit_code"] == 0 for row in commands)
        receipt.write_text(json.dumps(evidence, indent=2) + "\n")
        if evidence["success"]:
            return 0
    return 1


def check(directory):
    receipts = list(directory.glob("staple-*.json"))
    if not receipts:
        raise ValueError("no stapling receipt was recorded")
    for receipt in receipts:
        if json.loads(receipt.read_text()).get("success") is not True:
            raise ValueError(f"stapling did not validate; see {receipt}")


def main():
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--check":
            check(Path(sys.argv[2]))
            return 0
        directory = Path(os.environ["PW_STAPLE_LOG_DIR"])
        if sys.argv[1:3] == ["notarytool", "submit"]:
            return notarize(sys.argv[1:], directory)
        return staple(sys.argv[1:], directory)
    except (KeyError, OSError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
