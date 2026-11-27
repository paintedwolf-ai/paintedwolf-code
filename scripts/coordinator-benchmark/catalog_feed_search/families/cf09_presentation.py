"""CF09 native Den coverage presentation fixtures, verifier, and dual-host runners.

Drives native Den's search stage, quick open (Crossbar), replace review, and
Files tree through warming, refreshing, failed, and incomplete coverage states
against the unmodified D1 `lycaon-dev` binary (D6, D14).
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import time
from typing import Any

SURFACES: tuple[str, ...] = (
    "search_stage",
    "quick_open",
    "replace_review",
    "files_tree",
)

COVERAGE_STATES: tuple[str, ...] = (
    "warming",
    "refreshing",
    "failed",
    "incomplete",
)

NEEDLE_TOKEN = "CF09_COVERAGE_NEEDLE_20261127"

DECLARED_GAP_REASONS: frozenset[str] = frozenset(
    {
        "catalog_warming",
        "catalog_incomplete",
        "catalog_bounded",
        "catalog_failed",
        "catalog_refresh_failed",
        "catalog_refreshing",
        "executor_error",
        "index_warming",
        "files_skipped",
        "time_budget",
        "result_limit",
    }
)


@dataclass(frozen=True)
class CoverageFixture:
    """Materialized synthetic project root for one CF09 coverage state."""

    state: str
    root_dir: Path
    needle_file: Path
    locked_dir: Path | None = None
    file_count: int = 0

    def cleanup(self) -> None:
        """Restore permissions on locked directories before teardown."""
        if self.locked_dir is not None and self.locked_dir.exists():
            try:
                os.chmod(self.locked_dir, 0o755)
            except OSError:
                pass


def build_coverage_fixture(
    base_dir: Path,
    state: str,
    *,
    warming_file_count: int = 4000,
) -> CoverageFixture:
    """Materialize a real filesystem project root that induces `state` in D1 lycaon-dev."""
    if state not in COVERAGE_STATES:
        raise ValueError(f"unsupported CF09 coverage state: {state!r}")

    root = (base_dir / f"cf09_{state}").resolve()
    root.mkdir(parents=True, exist_ok=True)
    healthy = root / "healthy"
    healthy.mkdir(parents=True, exist_ok=True)
    needle_file = healthy / "target.txt"
    needle_file.write_text(
        f"line1: {NEEDLE_TOKEN}\nline2: fixture={state}\n",
        encoding="utf-8",
    )

    if state == "failed":
        locked = root / "locked_dir"
        locked.mkdir(parents=True, exist_ok=True)
        (locked / "hidden_secret.txt").write_text(
            f"unreadable: {NEEDLE_TOKEN}\n", encoding="utf-8"
        )
        os.chmod(locked, 0o000)
        return CoverageFixture(
            state=state,
            root_dir=root,
            needle_file=needle_file,
            locked_dir=locked,
            file_count=2,
        )

    if state == "incomplete":
        pw_dir = root / ".paintedwolf"
        pw_dir.mkdir(parents=True, exist_ok=True)
        (pw_dir / "source-scope.yaml").write_text(
            "version: 1\n"
            "budgets:\n"
            "  catalog:\n"
            "    directory_entries: 2\n"
            "    subtree_entries: 4\n"
            "    walk_entries: 6\n",
            encoding="utf-8",
        )
        wide = root / "wide_dir"
        wide.mkdir(parents=True, exist_ok=True)
        for i in range(10):
            (wide / f"item_{i:02d}.txt").write_text(
                f"wide {i} {NEEDLE_TOKEN}\n", encoding="utf-8"
            )
        return CoverageFixture(
            state=state,
            root_dir=root,
            needle_file=needle_file,
            file_count=11,
        )

    # warming and refreshing both use a multi-directory synthetic tree.
    # For warming, queries run immediately on first attach; for refreshing,
    # queries run immediately after a burst mutation on a settled tree.
    dirs = max(1, warming_file_count // 40)
    written = 1
    for d in range(dirs):
        sub = root / f"pkg_{d // 20:02d}" / f"mod_{d:04d}"
        sub.mkdir(parents=True, exist_ok=True)
        for f in range(40):
            if written >= warming_file_count:
                break
            (sub / f"f_{f:02d}.txt").write_text(
                f"module {d} file {f}\n", encoding="utf-8"
            )
            written += 1
        if written >= warming_file_count:
            break

    return CoverageFixture(
        state=state,
        root_dir=root,
        needle_file=needle_file,
        file_count=written,
    )


def mutate_for_refreshing(fixture: CoverageFixture, burst_count: int = 250) -> int:
    """Create a burst of new files in a settled tree to induce `catalog_refreshing`."""
    burst_dir = fixture.root_dir / "refresh_burst"
    burst_dir.mkdir(parents=True, exist_ok=True)
    for i in range(burst_count):
        (burst_dir / f"burst_{i:04d}.txt").write_text(
            f"burst {i} {NEEDLE_TOKEN}\n", encoding="utf-8"
        )
    return burst_count


def extract_declared_facts(api_response: dict[str, Any]) -> tuple[str, ...]:
    """Extract typed coverage facts from a D1 search, source/search, replace, or index response."""
    facts: set[str] = set()

    for issue in api_response.get("issues") or []:
        if isinstance(issue, dict):
            reason = str(issue.get("reason") or "")
            count = issue.get("count")
            if reason in DECLARED_GAP_REASONS and (count is None or int(count) > 0):
                facts.add(reason)

    state = str(api_response.get("state") or "")
    if state == "warming":
        facts.add("catalog_warming")
    elif state == "preparing":
        facts.add("catalog_warming")
    elif state == "failed":
        facts.add("executor_error")
    elif state == "limited":
        facts.add("limited_preview")

    if api_response.get("refreshing") is True:
        facts.add("catalog_refreshing")

    for root in api_response.get("coverage") or []:
        if not isinstance(root, dict):
            continue
        r_state = str(root.get("state") or "")
        r_err = str(root.get("error") or "")
        if r_err or r_state == "failed":
            facts.add("catalog_refresh_failed" if r_state == "ready" else "executor_error")
        elif root.get("discovery_complete") is False:
            facts.add("catalog_incomplete" if r_state == "ready" else "catalog_warming")
        elif root.get("refreshing") is True:
            facts.add("catalog_refreshing")
        if int(root.get("bounded_directories") or 0) > 0:
            facts.add("catalog_bounded")
        if int(root.get("failed_directories") or 0) > 0:
            facts.add("catalog_failed")

    return tuple(sorted(facts))


@dataclass(frozen=True)
class PresentationObservation:
    """Typed observation of one native Den surface in one coverage state."""

    surface: str
    state: str
    platform: str  # "linux" | "macos"
    native_window: bool
    api_response: dict[str, Any]
    coverage_note_visible: bool
    coverage_note_testid: str = ""
    results_visible_count: int = 0
    empty_final_shown: bool = False
    apply_all_disabled: bool | None = None
    screenshot_path: str = ""
    notes: str = ""


@dataclass(frozen=True)
class PresentationVerdict:
    """Verdict for one CF09 native Den surface × state slot."""

    surface: str
    state: str
    platform: str
    verdict: str  # "PASS" | "DEFECT" | "BLOCKED"
    declared_facts: tuple[str, ...]
    violations: tuple[str, ...] = ()
    screenshot_path: str = ""

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def verify_presentation_observation(obs: PresentationObservation) -> PresentationVerdict:
    """Verify CF09 acceptance rules against one typed native Den observation."""
    if obs.surface not in SURFACES:
        raise ValueError(f"unsupported surface: {obs.surface!r}")
    if obs.state not in COVERAGE_STATES:
        raise ValueError(f"unsupported state: {obs.state!r}")

    facts = extract_declared_facts(obs.api_response)
    violations: list[str] = []

    # D14: A browser page never satisfies a native slot.
    if not obs.native_window:
        violations.append("not_native_window: D14 requires a real native Den desktop window")

    # Rule 2: An empty result is never shown as final while a root is warming or failed.
    warming_or_failed = (
        obs.state in ("warming", "failed")
        or bool(
            set(facts)
            & {
                "catalog_warming",
                "catalog_failed",
                "catalog_refresh_failed",
                "executor_error",
            }
        )
    )
    if warming_or_failed and obs.empty_final_shown:
        violations.append(
            f"false_final_empty: empty result shown as final while state={obs.state} facts={list(facts)}"
        )

    # Rule 1: Every declared coverage fact reaches the screen beside its results.
    # Note: per docs/search.md § Crossbar, routine refresh of an already complete
    # index with no other issue may run without a status banner in compact lanes,
    # while explicit warming/incomplete/failed/bounded facts MUST be shown on screen.
    non_routine_facts = tuple(f for f in facts if f != "catalog_refreshing")
    if non_routine_facts and not obs.coverage_note_visible:
        violations.append(
            f"missing_coverage_presentation: declared facts {list(non_routine_facts)} did not reach the screen"
        )

    # Replace review contract (R01 / docs/search.md):
    # - `limited` preview disables Apply all.
    # - `preparing` preview never shows empty-final and disables Apply all.
    if obs.surface == "replace_review":
        preview_state = str(obs.api_response.get("state") or "")
        if preview_state in ("limited", "preparing"):
            if obs.empty_final_shown:
                violations.append(
                    f"replace_false_final_empty: empty shown during preview state={preview_state}"
                )
            if obs.apply_all_disabled is not True:
                violations.append(
                    f"replace_apply_all_enabled: Apply all must be disabled in preview state={preview_state}"
                )

    verdict = "PASS" if not violations else "DEFECT"
    return PresentationVerdict(
        surface=obs.surface,
        state=obs.state,
        platform=obs.platform,
        verdict=verdict,
        declared_facts=facts,
        violations=tuple(violations),
        screenshot_path=obs.screenshot_path,
    )


def write_presentation_evidence(
    out_dir: Path,
    *,
    platform: str,
    baseline_commit: str,
    binary_path: str,
    binary_sha256: str,
    verdicts: list[PresentationVerdict],
    extra_metadata: dict[str, Any] | None = None,
) -> Path:
    """Write retained CF09 presentation report and SHA-256 digest manifest."""
    out_dir.mkdir(parents=True, exist_ok=True)
    report = {
        "card": "CF09",
        "platform": platform,
        "baseline_commit": baseline_commit,
        "binary_path": binary_path,
        "binary_sha256": binary_sha256,
        "recorded_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "summary": {
            "total": len(verdicts),
            "passed": sum(1 for v in verdicts if v.verdict == "PASS"),
            "failed": sum(1 for v in verdicts if v.verdict == "DEFECT"),
            "blocked": sum(1 for v in verdicts if v.verdict == "BLOCKED"),
        },
        "verdicts": [v.to_dict() for v in verdicts],
        "metadata": extra_metadata or {},
    }
    report_path = out_dir / f"cf09_presentation_{platform}.json"
    report_path.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )

    digests: dict[str, str] = {}
    for path in sorted(out_dir.rglob("*")):
        if path.is_file() and path.name != "sha256_manifest.json":
            rel = path.relative_to(out_dir).as_posix()
            h = hashlib.sha256()
            with path.open("rb") as fh:
                for chunk in iter(lambda: fh.read(65536), b""):
                    h.update(chunk)
            digests[rel] = h.hexdigest()

    (out_dir / "sha256_manifest.json").write_text(
        json.dumps(digests, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    return report_path
