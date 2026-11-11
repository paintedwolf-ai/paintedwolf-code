"""Writes what a failed `./task` leaves on its streams, at the digest's size limit, for delivery contracts.

Usage: failure_digest_output.py <directory>. The digest alone also goes to <directory>/digest.txt.
"""

import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from verification_lifecycle import digest  # noqa: E402

LINKER = "ld: warning: ignoring duplicate libraries: '-lobjc'"


def main(directory):
    directory = Path(directory)
    packages = [f"example.invalid/internal/package{index}" for index in range(16)]
    events = []
    for package in packages:
        test = "TestDeliveredCause"
        events.append({"Action": "output", "Package": package, "Test": test,
                       "Output": "    cause_test.go:12: want é界🐺 " + "assertion context " * 40 + "\n"})
        events.append({"Action": "fail", "Package": package, "Test": test})
    raw = directory / "stage-001-go.json"
    raw.write_text("".join(json.dumps(event) + "\n" for event in events))
    log = directory / "stage-001.log"
    passes = "".join(f"[PASS] target-{index}\n{LINKER}\n" for index in range(6000))
    log.write_text(passes)
    results = {package: {"status": "failed", "exit_code": 1, "stage_id": "stage-001", "log": str(log), "raw": str(raw)}
               for package in packages}
    receipt = {"names": ["check-fast"], "status": "failed", "exit_code": 201,
               "evidence": [{"stage": "test:short", "results": results}],
               "unverified_stages": ["lint:full", "budgets", "den:typecheck", "den:test:fast"]}
    path = directory / "00000000000000000001-0123456789abcdef0123456789abcdef.json"
    path.write_text(json.dumps(receipt))
    notice = f"test execution: check-fast: failed (source 0123456789abcdef0123456789abcdef01234567; receipt {path})"
    block = digest.request_digest(receipt, path, directory, 1, late=notice)
    (directory / "digest.txt").write_text(block)
    sys.stdout.write("".join(line for line in passes.splitlines(keepends=True) if line.rstrip("\n") != LINKER))
    sys.stdout.flush()
    print(notice, file=sys.stderr, flush=True)
    digest.deliver(block)
    return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1]))
