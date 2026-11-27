"""In-process lane adapter (D6/D7/018) wrapping `./task test:digest` via invocation receipts & Go JSON."""

from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
import re
import subprocess
from typing import Callable

_RECEIPT_LINE = re.compile(r"^\s*Receipt:\s*(.+?)\s*$", re.MULTILINE | re.IGNORECASE)
_ALT_RECEIPT_LINE = re.compile(r"^\s*receipt\s+(\S+)\s*$", re.MULTILINE)


@dataclass(frozen=True)
class InProcessRunResult:
    """Structured result bound to a `./task test:digest` invocation receipt and Go test2json log.

    Decision 018: `qualified` is True ONLY when `exit_code == 0`, the invocation receipt
    exists and resolves its Go `test2json` NDJSON stage log, no test/package failed,
    `executed_cases > 0`, and the selected `pilot_test` has BOTH `Action:"run"` and
    terminal `Action:"pass"` records in Go JSON.
    """

    argv: tuple[str, ...]
    pilot_test: str
    exit_code: int
    qualified: bool
    verdict: str  # "PASS" | "FAIL" | "UNKNOWN"
    receipt_path: str
    source_digest: str
    go_json_paths: tuple[str, ...]
    executed_cases: int
    pilot_matched: bool
    passed_tests: tuple[str, ...]
    failed_tests: tuple[str, ...]
    passed_packages: tuple[str, ...]
    failed_packages: tuple[str, ...]
    reason: str = ""

    def to_dict(self) -> dict[str, object]:
        return {
            "argv": list(self.argv),
            "pilot_test": self.pilot_test,
            "exit_code": self.exit_code,
            "qualified": self.qualified,
            "verdict": self.verdict,
            "receipt_path": self.receipt_path,
            "source_digest": self.source_digest,
            "go_json_paths": list(self.go_json_paths),
            "executed_cases": self.executed_cases,
            "pilot_matched": self.pilot_matched,
            "passed_tests": list(self.passed_tests),
            "failed_tests": list(self.failed_tests),
            "passed_packages": list(self.passed_packages),
            "failed_packages": list(self.failed_packages),
            "reason": self.reason,
        }


def extract_receipt_path(stdout: str, stderr: str = "") -> str:
    """Extract the invocation receipt path printed by `./task test:digest`."""
    combined = f"{stdout}\n{stderr}"
    m = _RECEIPT_LINE.search(combined)
    if m:
        return m.group(1).strip()
    m2 = _ALT_RECEIPT_LINE.search(combined)
    if m2:
        return m2.group(1).strip()
    return ""


def _collect_go_json_paths(receipt: dict[str, object], receipt_dir: Path) -> tuple[str, ...]:
    paths: list[str] = []
    evidence = receipt.get("evidence")
    if isinstance(evidence, list):
        for stage in evidence:
            if not isinstance(stage, dict):
                continue
            results = stage.get("results")
            if not isinstance(results, dict):
                continue
            for pkg, res in sorted(results.items()):
                if pkg == "task" or not isinstance(res, dict):
                    continue
                raw = res.get("raw")
                if raw:
                    paths.append(str(raw))
                elif res.get("stage_id"):
                    paths.append(str(receipt_dir / f"{res['stage_id']}-go.json"))
    return tuple(dict.fromkeys(paths))


def parse_receipt_and_go_json(
    argv: tuple[str, ...],
    exit_code: int,
    stdout: str,
    stderr: str,
    *,
    pilot_test: str,
    receipt_path: Path | None = None,
) -> InProcessRunResult:
    """Verify an in-process pilot strictly from the invocation receipt JSON and Go test2json NDJSON."""
    resolved_str = str(receipt_path) if receipt_path is not None else extract_receipt_path(stdout, stderr)
    if not resolved_str or not Path(resolved_str).is_file():
        return InProcessRunResult(
            argv=argv,
            pilot_test=pilot_test,
            exit_code=exit_code,
            qualified=False,
            verdict="FAIL" if exit_code != 0 else "UNKNOWN",
            receipt_path=resolved_str,
            source_digest="",
            go_json_paths=(),
            executed_cases=0,
            pilot_matched=False,
            passed_tests=(),
            failed_tests=(),
            passed_packages=(),
            failed_packages=(),
            reason="invocation receipt missing or unreadable",
        )

    rpath = Path(resolved_str)
    receipt = json.loads(rpath.read_text(encoding="utf-8"))
    source_digest = str(
        receipt.get("source_commit") or receipt.get("source_digest") or receipt.get("source") or ""
    )
    go_json_paths = _collect_go_json_paths(receipt, rpath.parent)
    existing_logs = [Path(p) for p in go_json_paths if Path(p).is_file()]
    if not existing_logs:
        return InProcessRunResult(
            argv=argv,
            pilot_test=pilot_test,
            exit_code=exit_code,
            qualified=False,
            verdict="FAIL" if exit_code != 0 else "UNKNOWN",
            receipt_path=str(rpath),
            source_digest=source_digest,
            go_json_paths=go_json_paths,
            executed_cases=0,
            pilot_matched=False,
            passed_tests=(),
            failed_tests=(),
            passed_packages=(),
            failed_packages=(),
            reason="receipt references no readable Go test2json stage log",
        )

    run_tests: set[str] = set()
    passed_tests: set[str] = set()
    failed_tests: set[str] = set()
    passed_pkgs: set[str] = set()
    failed_pkgs: set[str] = set()

    for log_path in existing_logs:
        for line in log_path.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            if not isinstance(ev, dict):
                continue
            action = str(ev.get("Action") or "")
            test_name = str(ev.get("Test") or "")
            pkg_name = str(ev.get("Package") or "")
            if test_name:
                if action == "run":
                    run_tests.add(test_name)
                elif action == "pass":
                    passed_tests.add(test_name)
                elif action == "fail":
                    failed_tests.add(test_name)
            elif pkg_name:
                if action == "pass":
                    passed_pkgs.add(pkg_name)
                elif action == "fail":
                    failed_pkgs.add(pkg_name)

    executed_cases = len(passed_tests | failed_tests)
    pilot_ran = any(t == pilot_test or t.startswith(pilot_test + "/") for t in run_tests)
    pilot_passed = any(t == pilot_test or t.startswith(pilot_test + "/") for t in passed_tests)
    pilot_failed = any(t == pilot_test or t.startswith(pilot_test + "/") for t in failed_tests)
    pilot_matched = pilot_ran and pilot_passed and not pilot_failed

    if exit_code != 0 or failed_tests or failed_pkgs:
        verdict = "FAIL"
        qualified = False
        reason = "in-process pilot or package failed"
    elif executed_cases == 0 or not pilot_matched:
        verdict = "UNKNOWN"
        qualified = False
        reason = f"selected pilot {pilot_test!r} did not record both run and pass in Go JSON"
    else:
        verdict = "PASS"
        qualified = True
        reason = "receipt and Go test2json confirmed pilot run and pass"

    return InProcessRunResult(
        argv=argv,
        pilot_test=pilot_test,
        exit_code=exit_code,
        qualified=qualified,
        verdict=verdict,
        receipt_path=str(rpath),
        source_digest=source_digest,
        go_json_paths=go_json_paths,
        executed_cases=executed_cases,
        pilot_matched=pilot_matched,
        passed_tests=tuple(sorted(passed_tests)),
        failed_tests=tuple(sorted(failed_tests)),
        passed_packages=tuple(sorted(passed_pkgs)),
        failed_packages=tuple(sorted(failed_pkgs)),
        reason=reason,
    )


def build_task_digest_argv(
    repo_root: Path,
    package_pattern: str,
    *,
    pilot_test: str,
    count: int = 1,
) -> tuple[str, ...]:
    """Construct sanctioned `./task test:digest -- <pkg> -run ^<pilot>$` command."""
    if not pilot_test:
        raise ValueError("pilot_test must be a non-empty test selector")
    task_bin = str(Path(repo_root) / "task")
    argv = [task_bin, "test:digest", "--", package_pattern, "-run", f"^{pilot_test}$"]
    if count >= 1:
        argv.extend(["-count", str(count)])
    return tuple(argv)


def run_inprocess_suite(
    repo_root: Path,
    package_pattern: str,
    *,
    pilot_test: str,
    count: int = 1,
    executor: Callable[[tuple[str, ...], Path], tuple[int, str, str]] | None = None,
) -> InProcessRunResult:
    """Execute a scoped manual-clock / barrier Go pilot through `./task test:digest`."""
    argv = build_task_digest_argv(repo_root, package_pattern, pilot_test=pilot_test, count=count)
    if executor is not None:
        code, stdout, stderr = executor(argv, Path(repo_root))
    else:
        proc = subprocess.run(
            list(argv),
            cwd=str(repo_root),
            capture_output=True,
            text=True,
        )
        code, stdout, stderr = proc.returncode, proc.stdout, proc.stderr
    return parse_receipt_and_go_json(argv, code, stdout, stderr, pilot_test=pilot_test)
