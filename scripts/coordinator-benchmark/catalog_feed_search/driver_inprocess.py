"""In-process lane adapter (D6/D7) wrapping repo `./task test:digest` with pilot verification."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import re
import subprocess
from typing import Callable

_PASS_LINE = re.compile(r"^---\s+PASS:\s+(\S+)", re.MULTILINE)
_FAIL_LINE = re.compile(r"^---\s+FAIL:\s+(\S+)", re.MULTILINE)
_OK_PKG = re.compile(r"^ok\s+(\S+)", re.MULTILINE)
_FAIL_PKG = re.compile(r"^FAIL\s+(\S+)", re.MULTILINE)


@dataclass(frozen=True)
class InProcessRunResult:
    """Structured result of running a scoped in-process Go pilot via `./task test:digest`.

    Decision 015: `qualified` is True ONLY when `exit_code == 0`, no test/package failed,
    `executed_cases > 0`, and the required `pilot_test` was observed passing. Zero observed
    cases on exit 0 yields `verdict="UNKNOWN"` and `qualified=False`.
    """

    argv: tuple[str, ...]
    pilot_test: str
    exit_code: int
    qualified: bool
    verdict: str  # "PASS" | "FAIL" | "UNKNOWN"
    executed_cases: int
    pilot_matched: bool
    passed_tests: tuple[str, ...]
    failed_tests: tuple[str, ...]
    passed_packages: tuple[str, ...]
    failed_packages: tuple[str, ...]
    stdout: str
    stderr: str

    def to_dict(self) -> dict[str, object]:
        return {
            "argv": list(self.argv),
            "pilot_test": self.pilot_test,
            "exit_code": self.exit_code,
            "qualified": self.qualified,
            "verdict": self.verdict,
            "executed_cases": self.executed_cases,
            "pilot_matched": self.pilot_matched,
            "passed_tests": list(self.passed_tests),
            "failed_tests": list(self.failed_tests),
            "passed_packages": list(self.passed_packages),
            "failed_packages": list(self.failed_packages),
        }


def parse_digest_output(
    argv: tuple[str, ...],
    exit_code: int,
    stdout: str,
    stderr: str,
    *,
    pilot_test: str = "",
) -> InProcessRunResult:
    """Parse `./task test:digest` output and require observed executed cases + pilot match."""
    passed_tests = tuple(_PASS_LINE.findall(stdout))
    failed_tests = tuple(_FAIL_LINE.findall(stdout))
    passed_pkgs = tuple(_OK_PKG.findall(stdout))
    failed_pkgs = tuple(_FAIL_PKG.findall(stdout))
    executed_cases = len(passed_tests) + len(failed_tests)

    if pilot_test:
        pilot_matched = any(
            t == pilot_test or t.startswith(pilot_test + "/") for t in passed_tests
        )
    else:
        pilot_matched = executed_cases > 0

    if exit_code != 0 or failed_tests or failed_pkgs:
        verdict = "FAIL"
        qualified = False
    elif executed_cases == 0 or not pilot_matched:
        verdict = "UNKNOWN"
        qualified = False
    else:
        verdict = "PASS"
        qualified = True

    return InProcessRunResult(
        argv=argv,
        pilot_test=pilot_test,
        exit_code=exit_code,
        qualified=qualified,
        verdict=verdict,
        executed_cases=executed_cases,
        pilot_matched=pilot_matched,
        passed_tests=passed_tests,
        failed_tests=failed_tests,
        passed_packages=passed_pkgs,
        failed_packages=failed_pkgs,
        stdout=stdout,
        stderr=stderr,
    )


def build_task_digest_argv(
    repo_root: Path,
    package_pattern: str,
    *,
    pilot_test: str,
    count: int = 1,
) -> tuple[str, ...]:
    """Construct sanctioned `./task test:digest -- <pkg> -v -run ^<pilot>$` command."""
    if not pilot_test:
        raise ValueError("pilot_test must be a non-empty test selector")
    task_bin = str(Path(repo_root) / "task")
    argv = [task_bin, "test:digest", "--", package_pattern, "-v", "-run", f"^{pilot_test}$"]
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
    return parse_digest_output(argv, code, stdout, stderr, pilot_test=pilot_test)
