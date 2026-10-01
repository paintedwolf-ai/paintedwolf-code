"""Structured package evidence from a Go digest's exact invocation."""

import json
from pathlib import Path
import sys

from verification_state import write_json


def package_results(raw, process_code, digest_code):
    packages = {}
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            continue
        if not isinstance(event, dict):
            continue
        package = event.get("Package")
        if isinstance(package, str) and package and not event.get("Test") and event.get("Action") in ("pass", "fail", "skip"):
            packages[package] = event["Action"]
    # Only normal test exits establish package outcomes.
    valid = bool(packages) and process_code in {0, 1} and digest_code in {0, 1}
    if process_code != 0 or digest_code != 0:
        valid = valid and "fail" in packages.values()
    return {"process_exit_code": process_code, "digest_exit_code": digest_code,
            "completed": valid, "packages": packages}


def main():
    output, raw, process_code, digest_code, manifest = sys.argv[1:]
    result = package_results(Path(raw).read_text(errors="replace"), int(process_code), int(digest_code))
    result["invocation"] = json.loads(Path(manifest).read_text())
    write_json(output, result)


if __name__ == "__main__":
    main()
