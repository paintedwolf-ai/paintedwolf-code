#!/usr/bin/env python3
"""Render test runner JSON failures."""

from __future__ import annotations

import argparse
import json
import os
import sys

from verification_timings import seconds, write_slowest


_CONSOLE_TAIL_LINES = 12
_STACK_LINES_PER_ERROR = 4


def _read_outcome(path: str | None) -> dict | None:
    """Return the end-of-run record when available."""
    if not path or not os.path.isfile(path):
        return None
    try:
        with open(path, encoding="utf-8", errors="replace") as fh:
            outcome = json.load(fh)
    except (OSError, ValueError):
        return None
    return outcome if isinstance(outcome, dict) else None


def _unhandled_error_lines(outcome: dict) -> list[str]:
    """Render unhandled errors absent from the JSON report."""
    errors = outcome.get("unhandledErrors")
    if not isinstance(errors, list) or not errors:
        return []
    noun = "error" if len(errors) == 1 else "errors"
    lines = [f"vitest reported {len(errors)} unhandled {noun} outside any test:"]
    for err in errors:
        if not isinstance(err, dict):
            continue
        name = str(err.get("name") or "Error")
        message = str(err.get("message") or "").strip()
        lines.append(f"  {name}: {message}" if message else f"  {name}")
        stack = err.get("stack")
        if isinstance(stack, str):
            frames = [ln.strip() for ln in stack.splitlines() if ln.strip().startswith("at ")]
            lines.extend(f"    {frame}" for frame in frames[:_STACK_LINES_PER_ERROR])
    return lines


def _assertion_detail_lines(
    outcome: dict | None, file_name: str, full_name: str
) -> list[str]:
    """Return complete actual and expected values."""
    if outcome is None:
        return []
    failed = outcome.get("failedTests")
    if not isinstance(failed, list):
        return []
    normalized_name = full_name.replace(" > ", " ")
    for test in failed:
        if not isinstance(test, dict) or test.get("file") != file_name:
            continue
        recorded_name = str(test.get("name") or "").replace(" > ", " ")
        if recorded_name != normalized_name:
            continue
        lines: list[str] = []
        errors = test.get("errors")
        if not isinstance(errors, list):
            return lines
        for error in errors:
            if not isinstance(error, dict):
                continue
            for label in ("actual", "expected"):
                value = error.get(label)
                if isinstance(value, str):
                    lines.append(f"{label}:")
                    lines.extend(f"  {line}" for line in value.splitlines())
        return lines
    return []


def _failure_explanation(outcome_path: str | None, stdout_path: str | None) -> list[str]:
    """Explain a nonzero run with no failed assertion."""
    outcome = _read_outcome(outcome_path)
    if outcome is None:
        return _console_signal(stdout_path)
    errors = _unhandled_error_lines(outcome)
    if errors:
        return errors
    reason = outcome.get("reason")
    heading = (
        [f"vitest ended the run as {reason!r} with no unhandled error recorded"]
        if isinstance(reason, str)
        else []
    )
    return heading + _console_signal(stdout_path)


def _console_signal(path: str | None) -> list[str]:
    """Return the captured console tail."""
    if not path or not os.path.isfile(path):
        return ["(no console output captured)"]
    try:
        with open(path, encoding="utf-8", errors="replace") as fh:
            lines = [ln.rstrip() for ln in fh.readlines()]
    except OSError:
        return ["(console output could not be read)"]
    tail = [ln for ln in lines if ln.strip()][-_CONSOLE_TAIL_LINES:]
    if not tail:
        return ["(console output was empty)"]
    return ["(tail of console output follows)", *tail]


def _write_failed_files(last_run_dir: str | None, files: set[str]) -> None:
    if not last_run_dir or not files:
        return
    os.makedirs(last_run_dir, exist_ok=True)
    path = os.path.join(last_run_dir, "failed-den-files.txt")
    with open(path, "w", encoding="utf-8") as fh:
        for name in sorted(files):
            fh.write(f"{name}\n")


def _validate_report(report) -> None:
    if not isinstance(report, dict):
        raise ValueError("report must be an object")
    for field in ("numTotalTests", "numPassedTests"):
        if type(report.get(field)) is not int or report[field] < 0:
            raise ValueError(f"report requires a nonnegative {field}")
    if report["numPassedTests"] > report["numTotalTests"]:
        raise ValueError("passed test count exceeds total test count")
    if "success" in report and type(report["success"]) is not bool:
        raise ValueError("report success must be a boolean")
    if "numFailedTests" in report and (type(report["numFailedTests"]) is not int or report["numFailedTests"] < 0):
        raise ValueError("report requires a nonnegative numFailedTests")
    if not isinstance(report.get("testResults"), list):
        raise ValueError("report requires testResults")
    for suite in report["testResults"]:
        if not isinstance(suite, dict) or not isinstance(suite.get("assertionResults", []), list):
            raise ValueError("invalid test suite")
        for field in ("name", "status", "message"):
            if not isinstance(suite.get(field, ""), str):
                raise ValueError(f"invalid suite {field}")
        for assertion in suite.get("assertionResults", []):
            if not isinstance(assertion, dict):
                raise ValueError("invalid test assertion")
            for field in ("fullName", "title", "status"):
                if not isinstance(assertion.get(field, ""), str):
                    raise ValueError(f"invalid assertion {field}")
            messages = assertion.get("failureMessages", [])
            if not isinstance(messages, list) or not all(isinstance(message, str) for message in messages):
                raise ValueError("invalid assertion failure messages")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--name", default="den:test", help="label for this run in the digest header")
    parser.add_argument("--input", required=True, help="path to vitest JSON report")
    parser.add_argument(
        "--raw-log",
        help="repo-relative path to the JSON log (shown on failure)",
    )
    parser.add_argument(
        "--lines",
        type=int,
        default=12,
        help="max assertion lines per failing test (default: 12)",
    )
    parser.add_argument(
        "--vitest-rc",
        type=int,
        default=0,
        help="vitest's exit code, so a non-zero exit with zero failing tests is not reported green",
    )
    parser.add_argument(
        "--stdout-log",
        help="path to captured vitest console output (read when the exit code needs explaining)",
    )
    parser.add_argument(
        "--stdout-log-path",
        help="repo-relative path to the captured console output (shown to the reader)",
    )
    parser.add_argument(
        "--outcome",
        help="path to the outcome reporter's JSON (vitest's own reason + unhandled errors)",
    )
    args = parser.parse_args(argv)

    last_run_dir = os.environ.get("LAST_RUN_DIR")
    raw_path = args.raw_log or args.input
    outcome = _read_outcome(args.outcome)

    try:
        with open(args.input, encoding="utf-8") as fh:
            report = json.load(fh)
        _validate_report(report)
    except (OSError, ValueError) as err:
        sys.stdout.write(f"\n  ✗ {args.name}\n")
        sys.stdout.write(f"    could not read vitest report: {err}\n")
        if raw_path:
            sys.stdout.write(f"    full log: {raw_path}\n")
        return 1

    out = sys.stdout
    failed_files: set[str] = set()
    failed_tests: list[tuple[str, str, list[str]]] = []
    # Collection and file-level failures have no failed assertion.
    file_only_failures: list[tuple[str, str]] = []

    for suite in report.get("testResults", []):
        file_name = suite.get("name", "")
        status = suite.get("status", "")
        suite_failed_assertions = [
            a for a in suite.get("assertionResults", []) if a.get("status") == "failed"
        ]
        if status == "failed" and file_name:
            failed_files.add(file_name)
            if not suite_failed_assertions:
                file_only_failures.append((file_name, (suite.get("message") or "").strip()))

        for assertion in suite_failed_assertions:
            full_name = assertion.get("fullName") or assertion.get("title") or "unknown test"
            messages = assertion.get("failureMessages") or []
            if suite.get("message") and not messages:
                messages = [suite["message"]]
            failed_tests.append((file_name or "unknown file", full_name, messages))
            if file_name:
                failed_files.add(file_name)

    num_total = report.get("numTotalTests")
    num_passed = report.get("numPassedTests")

    def write_timings():
        timings = []
        for suite in report["testResults"]:
            # Assertion durations exclude transform, collection, and environment setup.
            name = suite.get("name", "unknown file").replace("\\", "/")
            if "/lycaon-den/" in name:
                name = name.split("/lycaon-den/", 1)[1]
            for assertion in suite.get("assertionResults", []):
                if assertion.get("status") in {"passed", "failed"}:
                    test = assertion.get("fullName") or assertion.get("title") or "unknown test"
                    timings.append((f"{name} {test}", seconds(assertion.get("duration"), 1000)))
        write_slowest(out, "tests", timings)

    if file_only_failures and not failed_tests:
        out.write(f"\n  ✗ {args.name} — {len(file_only_failures)} test file(s) failed to run\n")
        out.write("    Every collected test passed; these files failed before or outside any test\n")
        out.write("    (collection/import error or a file-level throw), so they contribute no test counts.\n")
        for file_name, message in file_only_failures:
            out.write(f"    {file_name}\n")
            for ln in message.splitlines()[: args.lines]:
                out.write(f"      {ln.rstrip()}\n")
        for line in _failure_explanation(args.outcome, args.stdout_log):
            out.write(f"      {line}\n")
        if args.stdout_log_path:
            out.write(f"    console log: {args.stdout_log_path}\n")
        if raw_path:
            out.write(f"    json report: {raw_path}\n")
        _write_failed_files(last_run_dir, failed_files)
        return 1

    if not failed_tests:
        summary = f"{num_passed}/{num_total} tests passed" if num_total is not None else "no failures"
        reported_failure = report.get("success") is False or bool(report.get("numFailedTests"))
        if outcome:
            reported_failure = (reported_failure or bool(outcome.get("unhandledErrors"))
                                or outcome.get("reason") in ("failed", "interrupted"))
        if args.vitest_rc != 0 or reported_failure:
            # The structured report omits runner-level failures.
            cause = f"vitest exited {args.vitest_rc}" if args.vitest_rc else "the runner reported failure"
            out.write(f"\n  ✗ {args.name} — {summary}, but {cause}\n")
            out.write("    No failed assertion explains the runner failure.\n")
            for line in _failure_explanation(args.outcome, args.stdout_log):
                out.write(f"      {line}\n")
            if args.stdout_log_path:
                out.write(f"    console log: {args.stdout_log_path}\n")
            if raw_path:
                out.write(f"    json report: {raw_path}\n")
            return 1
        out.write(f"\n  ✓ {args.name} — {summary}\n")
        write_timings()
        if last_run_dir:
            failed_path = os.path.join(last_run_dir, "failed-den-files.txt")
            if os.path.isfile(failed_path):
                os.remove(failed_path)
        return 0

    by_file: dict[str, list[tuple[str, list[str]]]] = {}
    for file_name, full_name, messages in failed_tests:
        by_file.setdefault(file_name, []).append((full_name, messages))

    out.write(f"\n  ✗ {args.name}\n")
    for file_name in sorted(by_file):
        out.write(f"    {file_name}\n")
        for full_name, messages in by_file[file_name]:
            out.write(f"      {full_name}\n")
            for msg in messages[:1]:
                for ln in msg.splitlines()[: args.lines]:
                    out.write(f"        {ln.rstrip()}\n")
            for ln in _assertion_detail_lines(outcome, file_name, full_name):
                out.write(f"        {ln}\n")

    out.write(
        f"    → {len(failed_tests)} failing test{'s' if len(failed_tests) != 1 else ''} "
        f"across {len(by_file)} file{'s' if len(by_file) != 1 else ''}\n"
    )
    if raw_path:
        out.write(f"    full log: {raw_path}\n")

    _write_failed_files(last_run_dir, failed_files)
    write_timings()
    return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
