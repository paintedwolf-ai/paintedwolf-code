"""In-process lane adapter (D6/D7/018) wrapping `./task test:digest` via receipts & Go JSON."""

from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
import re
import subprocess
import sys
from typing import Callable

_RECEIPT_LINE = re.compile(r"^\s*Receipt:\s*(.+?)\s*$", re.MULTILINE | re.IGNORECASE)
_ALT_RECEIPT_LINE = re.compile(r"\breceipt\s+([^\s);]+)")


@dataclass(frozen=True)
class InProcessRunResult:
    """Structured result bound to a `./task test:digest` invocation receipt and Go test2json log.

    Decision 018: `qualified` is True ONLY when `exit_code == 0`, a shared-batch receipt
    or freshly written `last-run/go-test.json` resolves Go `test2json` NDJSON records,
    no test/package failed, `executed_cases > 0`, and the selected `pilot_test` has BOTH
    `Action:"run"` and terminal `Action:"pass"` records in Go JSON.
    """

    argv: tuple[str, ...]
    pilot_test: str
    exit_code: int
    qualified: bool
    verdict: str  # "PASS" | "FAIL" | "UNKNOWN"
    receipt_mode: str  # "shared_batch_receipt" | "last_run_go_json" | "missing"
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
            "receipt_mode": self.receipt_mode,
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


def resolve_last_run_go_json(repo_root: Path) -> Path | None:
    """Resolve `<artifact_root>/last-run/go-test.json` via `scripts/artifact_paths.py root`."""
    script = Path(repo_root) / "scripts" / "artifact_paths.py"
    if not script.is_file():
        return None
    proc = subprocess.run(
        [sys.executable, str(script), "root"],
        cwd=str(repo_root),
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0 or not proc.stdout.strip():
        return None
    return Path(proc.stdout.strip()) / "last-run" / "go-test.json"


def _git_head(repo_root: Path) -> str:
    proc = subprocess.run(
        ["git", "-C", str(repo_root), "rev-parse", "HEAD"],
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.stdout.strip() if proc.returncode == 0 else ""


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


def parse_go_json_logs(
    argv: tuple[str, ...],
    exit_code: int,
    log_paths: list[Path],
    *,
    pilot_test: str,
    receipt_mode: str,
    receipt_path: str,
    source_digest: str,
) -> InProcessRunResult:
    """Parse Go `test2json` NDJSON logs and require pilot run + terminal pass."""
    run_tests: set[str] = set()
    passed_tests: set[str] = set()
    failed_tests: set[str] = set()
    passed_pkgs: set[str] = set()
    failed_pkgs: set[str] = set()

    for log_path in log_paths:
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
        reason = f"{receipt_mode} Go test2json confirmed pilot run and pass"

    return InProcessRunResult(
        argv=argv,
        pilot_test=pilot_test,
        exit_code=exit_code,
        qualified=qualified,
        verdict=verdict,
        receipt_mode=receipt_mode,
        receipt_path=receipt_path,
        source_digest=source_digest,
        go_json_paths=tuple(str(p) for p in log_paths),
        executed_cases=executed_cases,
        pilot_matched=pilot_matched,
        passed_tests=tuple(sorted(passed_tests)),
        failed_tests=tuple(sorted(failed_tests)),
        passed_packages=tuple(sorted(passed_pkgs)),
        failed_packages=tuple(sorted(failed_pkgs)),
        reason=reason,
    )


def parse_receipt_and_go_json(
    argv: tuple[str, ...],
    exit_code: int,
    stdout: str,
    stderr: str,
    *,
    pilot_test: str,
    receipt_path: Path | None = None,
    fresh_last_run_go_json: Path | None = None,
    fallback_source_digest: str = "",
) -> InProcessRunResult:
    """Verify an in-process pilot from shared-batch receipt JSON or freshly written last-run Go JSON."""
    resolved_str = str(receipt_path) if receipt_path is not None else extract_receipt_path(stdout, stderr)
    if resolved_str and Path(resolved_str).is_file():
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
                receipt_mode="shared_batch_receipt",
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
        return parse_go_json_logs(
            argv,
            exit_code,
            existing_logs,
            pilot_test=pilot_test,
            receipt_mode="shared_batch_receipt",
            receipt_path=str(rpath),
            source_digest=source_digest,
        )

    if fresh_last_run_go_json is not None and fresh_last_run_go_json.is_file():
        return parse_go_json_logs(
            argv,
            exit_code,
            [fresh_last_run_go_json],
            pilot_test=pilot_test,
            receipt_mode="last_run_go_json",
            receipt_path="",
            source_digest=fallback_source_digest,
        )

    return InProcessRunResult(
        argv=argv,
        pilot_test=pilot_test,
        exit_code=exit_code,
        qualified=False,
        verdict="FAIL" if exit_code != 0 else "UNKNOWN",
        receipt_mode="missing",
        receipt_path=resolved_str,
        source_digest="",
        go_json_paths=(),
        executed_cases=0,
        pilot_matched=False,
        passed_tests=(),
        failed_tests=(),
        passed_packages=(),
        failed_packages=(),
        reason="invocation receipt and fresh last-run/go-test.json both missing",
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


def _file_stamp(path: Path | None) -> tuple[int, int] | None:
    if path is None or not path.is_file():
        return None
    st = path.stat()
    return (st.st_mtime_ns, st.st_size)


def run_inprocess_suite(
    repo_root: Path,
    package_pattern: str,
    *,
    pilot_test: str,
    count: int = 1,
    last_run_go_json: Path | None = None,
    executor: Callable[[tuple[str, ...], Path], tuple[int, str, str]] | None = None,
) -> InProcessRunResult:
    """Execute a scoped manual-clock / barrier Go pilot through `./task test:digest`."""
    argv = build_task_digest_argv(repo_root, package_pattern, pilot_test=pilot_test, count=count)
    candidate_last_run = last_run_go_json if last_run_go_json is not None else resolve_last_run_go_json(repo_root)
    before_stamp = _file_stamp(candidate_last_run)

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

    after_stamp = _file_stamp(candidate_last_run)
    fresh_last_run = candidate_last_run if (after_stamp is not None and after_stamp != before_stamp) else None
    return parse_receipt_and_go_json(
        argv,
        code,
        stdout,
        stderr,
        pilot_test=pilot_test,
        fresh_last_run_go_json=fresh_last_run,
        fallback_source_digest=_git_head(repo_root),
    )
