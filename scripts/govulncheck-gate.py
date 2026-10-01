#!/usr/bin/env python3
"""Run govulncheck on lycaon/ and fail on unallowlisted symbol-level findings."""
from __future__ import annotations

import os
import re
import subprocess
import sys
import threading
from pathlib import Path

from artifact_paths import bin_dir

ROOT = Path(__file__).resolve().parent.parent
GO_DIR = ROOT / "lycaon"
BIN_DIR = bin_dir(ROOT)
GOVULNCHECK = BIN_DIR / "govulncheck"
ALLOWLIST = GO_DIR / "govulncheck-allowlist.yaml"
# The vendored database makes the verdict follow the source. Refresh the pin with
# ./task lint:vuln:vendor; upstream drift is caught by lint:vuln:fresh.
VULNDB = GO_DIR / "vulndb"
GOVULNCHECK_VERSION = "v1.3.0"
VULN_RE = re.compile(r"^Vulnerability #\d+: (GO-\d{4}-\d+)")
ALLOW_ID_RE = re.compile(r"^\s{2}(GO-\d{4}-\d+):")
REASON_RE = re.compile(r"^\s{4}reason:\s*(.+)")


def go_toolchain() -> str:
    for line in (GO_DIR / "go.mod").read_text(encoding="utf-8").splitlines():
        if line.startswith("go "):
            return f"go{line.split()[1]}"
    raise SystemExit("govulncheck gate: missing go directive in lycaon/go.mod")


def ensure_govulncheck(toolchain: str) -> None:
    BIN_DIR.mkdir(parents=True, exist_ok=True)
    if GOVULNCHECK.is_file() and os.access(GOVULNCHECK, os.X_OK):
        return
    print(
        f"Installing govulncheck {GOVULNCHECK_VERSION} to {BIN_DIR} (toolchain {toolchain})...",
        file=sys.stderr,
    )
    env = os.environ.copy()
    env["GOBIN"] = str(BIN_DIR)
    env["GOTOOLCHAIN"] = toolchain
    subprocess.run(
        [
            "go",
            "install",
            f"golang.org/x/vuln/cmd/govulncheck@{GOVULNCHECK_VERSION}",
        ],
        cwd=GO_DIR,
        env=env,
        check=True,
    )


def load_allowlist() -> dict[str, str]:
    if not ALLOWLIST.is_file():
        raise SystemExit(f"govulncheck gate: missing allowlist {ALLOWLIST}")

    allowed: dict[str, str] = {}
    current_id: str | None = None
    reason_lines: list[str] = []

    def flush() -> None:
        nonlocal current_id, reason_lines
        if current_id is None:
            return
        reason = " ".join(part.strip() for part in reason_lines).strip()
        if not reason:
            raise SystemExit(
                f"govulncheck gate: allowlist entry {current_id} missing reason"
            )
        allowed[current_id] = reason
        current_id = None
        reason_lines = []

    for raw in ALLOWLIST.read_text(encoding="utf-8").splitlines():
        line = raw.rstrip()
        if line.strip().startswith("#") or not line.strip():
            continue
        id_match = ALLOW_ID_RE.match(line)
        if id_match:
            flush()
            current_id = id_match.group(1)
            continue
        if current_id is not None:
            reason_match = REASON_RE.match(line)
            if reason_match:
                reason_lines.append(reason_match.group(1).strip())
            elif line.startswith("    reviewed:"):
                continue
            elif line.startswith("    "):
                reason_lines.append(line.strip())

    flush()
    return allowed


def database_url() -> str:
    """LYCAON_VULNDB points the gate at upstream for the scheduled freshness run."""
    override = os.environ.get("LYCAON_VULNDB", "").strip()
    if override:
        return override
    if not (VULNDB / "index" / "modules.json").is_file():
        raise SystemExit(
            "govulncheck gate: no vendored database at "
            f"{VULNDB}; run ./task lint:vuln:vendor"
        )
    return VULNDB.as_uri()


def run_govulncheck(toolchain: str) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["GOTOOLCHAIN"] = toolchain
    # Stream as it is produced; a run killed by a deadline otherwise leaves an
    # empty log.
    process = subprocess.Popen(
        [str(GOVULNCHECK), "-db", database_url(), "-test", "./..."],
        cwd=GO_DIR,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )
    captured = {"stdout": [], "stderr": []}

    def pump(source, sink, target):
        for line in source:
            captured[target].append(line)
            sink.write(line)
            sink.flush()

    threads = [
        threading.Thread(target=pump, args=(process.stdout, sys.stdout, "stdout"), daemon=True),
        threading.Thread(target=pump, args=(process.stderr, sys.stderr, "stderr"), daemon=True),
    ]
    for thread in threads:
        thread.start()
    code = process.wait()
    for thread in threads:
        thread.join()
    return subprocess.CompletedProcess(
        process.args, code, "".join(captured["stdout"]), "".join(captured["stderr"])
    )


def parse_reported_ids(output: str) -> list[str]:
    ids: list[str] = []
    seen: set[str] = set()
    for line in output.splitlines():
        match = VULN_RE.match(line)
        if not match:
            continue
        vuln_id = match.group(1)
        if vuln_id not in seen:
            seen.add(vuln_id)
            ids.append(vuln_id)
    return ids


def main() -> int:
    toolchain = go_toolchain()
    ensure_govulncheck(toolchain)
    allowed = load_allowlist()

    proc = run_govulncheck(toolchain)
    reported = parse_reported_ids(proc.stdout)
    if proc.returncode not in (0, 3):
        return proc.returncode if proc.returncode is not None else 1

    unallowlisted = [vid for vid in reported if vid not in allowed]
    stale = sorted(set(allowed) - set(reported))

    if unallowlisted:
        print("govulncheck: unallowlisted vulnerabilities affecting this module:", file=sys.stderr)
        for vid in unallowlisted:
            print(f"  - {vid}  https://pkg.go.dev/vuln/{vid}", file=sys.stderr)
        print(
            "Fix by upgrading dependencies or add a reviewed entry to "
            f"{ALLOWLIST.relative_to(ROOT)}.",
            file=sys.stderr,
        )
        sys.stdout.write(proc.stdout)
        return 1

    if stale:
        print(
            "govulncheck gate: allowlist entries no longer reported (remove them): "
            + ", ".join(stale),
            file=sys.stderr,
        )
        return 1

    if reported:
        print(
            "govulncheck: clean ("
            + ", ".join(f"{vid} allowlisted" for vid in reported)
            + ")",
            file=sys.stderr,
        )
    else:
        print("govulncheck: no vulnerabilities affecting this module", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
