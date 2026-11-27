"""Generic delta-debugging (ddmin) shrinker for failing campaign instances (D5)."""

from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
from typing import Callable, Sequence, TypeVar

from .change_runner import ChangeOp, ChangeScript
from .generators import TreeEntrySpec, TreeSpec
from .queries import CodeQuerySpec
from .seeds import ReplaySelector

T = TypeVar("T")


@dataclass(frozen=True)
class FailingInstance:
    """A failing seeded instance ready to be shrunk and replayed."""

    selector: ReplaySelector
    tree_spec: TreeSpec
    change_script: ChangeScript | None = None
    query_spec: CodeQuerySpec | None = None
    failure_summary: str = ""

    def to_dict(self) -> dict[str, object]:
        return {
            "selector": self.selector.to_dict(),
            "tree_spec": self.tree_spec.to_dict(),
            "change_script": self.change_script.to_dict() if self.change_script else None,
            "query_spec": self.query_spec.to_dict() if self.query_spec else None,
            "failure_summary": self.failure_summary,
        }

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "FailingInstance":
        cs_data = data.get("change_script")
        qs_data = data.get("query_spec")
        return cls(
            selector=ReplaySelector.from_dict(dict(data["selector"])),  # type: ignore[arg-type]
            tree_spec=TreeSpec.from_dict(dict(data["tree_spec"])),  # type: ignore[arg-type]
            change_script=ChangeScript.from_dict(dict(cs_data)) if cs_data else None,  # type: ignore[arg-type]
            query_spec=CodeQuerySpec.from_dict(dict(qs_data)) if qs_data else None,  # type: ignore[arg-type]
            failure_summary=str(data.get("failure_summary", "")),
        )


@dataclass(frozen=True)
class ShrinkResult:
    """Outcome of shrinking a failing instance to a minimal reproducer."""

    original_entry_count: int
    shrunk_entry_count: int
    original_op_count: int
    shrunk_op_count: int
    evaluations: int
    shrunk: FailingInstance

    def to_json(self) -> str:
        payload = {
            "selector": self.shrunk.selector.to_dict(),
            "replay_cli": " ".join(self.shrunk.selector.cli_args()),
            "original_entry_count": self.original_entry_count,
            "shrunk_entry_count": self.shrunk_entry_count,
            "original_op_count": self.original_op_count,
            "shrunk_op_count": self.shrunk_op_count,
            "evaluations": self.evaluations,
            "shrunk_instance": self.shrunk.to_dict(),
        }
        return json.dumps(payload, indent=2, sort_keys=True)

    def write_json(self, path: Path) -> None:
        Path(path).write_text(self.to_json() + "\n", encoding="utf-8")


def ddmin(
    items: Sequence[T],
    still_fails: Callable[[list[T]], bool],
    max_evaluations: int = 2000,
) -> tuple[list[T], int]:
    """Zeller's ddmin algorithm: shrink items to a 1-minimal failing subsequence."""
    current = list(items)
    evals = 0
    if len(current) <= 1:
        return current, evals

    n = 2
    while len(current) >= 2 and evals < max_evaluations:
        chunk_size = max(1, (len(current) + n - 1) // n)
        chunks = [current[i : i + chunk_size] for i in range(0, len(current), chunk_size)]
        reduced = False

        # First try each chunk on its own.
        for chunk in chunks:
            if evals >= max_evaluations:
                break
            evals += 1
            if still_fails(chunk):
                current = chunk
                n = 2
                reduced = True
                break
        if reduced:
            continue

        # Next try complements (removing one chunk at a time).
        for idx in range(len(chunks)):
            if evals >= max_evaluations:
                break
            complement = [item for j, ch in enumerate(chunks) if j != idx for item in ch]
            if not complement:
                continue
            evals += 1
            if still_fails(complement):
                current = complement
                n = max(n - 1, 2)
                reduced = True
                break
        if reduced:
            continue

        if n >= len(current):
            break
        n = min(len(current), n * 2)

    return current, evals


def _shrink_file_contents(
    instance: FailingInstance,
    predicate: Callable[[FailingInstance], bool],
    budget: int,
) -> tuple[FailingInstance, int]:
    """Shrink text file contents line-by-line while preserving the failure."""
    evals = 0
    entries = list(instance.tree_spec.entries)
    for idx, entry in enumerate(entries):
        if evals >= budget or entry.kind != "file" or not entry.content:
            continue
        lines = entry.content.splitlines(keepends=True)
        if len(lines) <= 1:
            continue

        def lines_fail(subset: list[bytes], entry_idx: int = idx, base_entry: TreeEntrySpec = entry) -> bool:
            if not subset:
                return False
            trial_entries = list(entries)
            trial_entries[entry_idx] = TreeEntrySpec(
                path=base_entry.path,
                kind=base_entry.kind,
                content=b"".join(subset),
                link_target=base_entry.link_target,
                mode=base_entry.mode,
            )
            trial = FailingInstance(
                selector=instance.selector,
                tree_spec=TreeSpec(selector=instance.selector, entries=tuple(trial_entries)),
                change_script=instance.change_script,
                query_spec=instance.query_spec,
                failure_summary=instance.failure_summary,
            )
            return predicate(trial)

        shrunk_lines, used = ddmin(lines, lines_fail, max_evaluations=max(1, budget - evals))
        evals += used
        if len(shrunk_lines) < len(lines):
            entries[idx] = TreeEntrySpec(
                path=entry.path,
                kind=entry.kind,
                content=b"".join(shrunk_lines),
                link_target=entry.link_target,
                mode=entry.mode,
            )
            instance = FailingInstance(
                selector=instance.selector,
                tree_spec=TreeSpec(selector=instance.selector, entries=tuple(entries)),
                change_script=instance.change_script,
                query_spec=instance.query_spec,
                failure_summary=instance.failure_summary,
            )
    return instance, evals


def _shrink_query(
    instance: FailingInstance,
    predicate: Callable[[FailingInstance], bool],
    budget: int,
) -> tuple[FailingInstance, int]:
    """Simplify optional query filters sequentially while preserving the failure."""
    evals = 0
    if instance.query_spec is None or budget <= 0:
        return instance, evals

    for attr in ("exclude_globs", "include_globs", "path_filters"):
        if evals >= budget:
            break
        q = instance.query_spec
        if q is None or not getattr(q, attr):
            continue
        updated = {
            "form": q.form,
            "pattern": q.pattern,
            "case_sensitive": q.case_sensitive,
            "whole_word": q.whole_word,
            "regex": q.regex,
            "include_globs": q.include_globs,
            "exclude_globs": q.exclude_globs,
            "path_filters": q.path_filters,
        }
        updated[attr] = ()
        trial = FailingInstance(
            selector=instance.selector,
            tree_spec=instance.tree_spec,
            change_script=instance.change_script,
            query_spec=CodeQuerySpec(**updated),  # type: ignore[arg-type]
            failure_summary=instance.failure_summary,
        )
        evals += 1
        if predicate(trial):
            instance = trial

    return instance, evals


def shrink_instance(
    instance: FailingInstance,
    predicate: Callable[[FailingInstance], bool],
    max_evaluations: int = 1000,
) -> ShrinkResult:
    """Shrink tree entries, file contents, change ops, and query while predicate holds."""
    if not predicate(instance):
        raise ValueError("initial instance does not satisfy the failure predicate")

    orig_entries = len(instance.tree_spec.entries)
    orig_ops = len(instance.change_script.ops) if instance.change_script else 0
    total_evals = 1
    current = instance

    # 1. Shrink tree entries.
    def entries_fail(subset: list[TreeEntrySpec]) -> bool:
        trial = FailingInstance(
            selector=current.selector,
            tree_spec=TreeSpec(selector=current.selector, entries=tuple(subset)),
            change_script=current.change_script,
            query_spec=current.query_spec,
            failure_summary=current.failure_summary,
        )
        return predicate(trial)

    shrunk_entries, used = ddmin(
        current.tree_spec.entries,
        entries_fail,
        max_evaluations=max(1, max_evaluations - total_evals),
    )
    total_evals += used
    current = FailingInstance(
        selector=current.selector,
        tree_spec=TreeSpec(selector=current.selector, entries=tuple(shrunk_entries)),
        change_script=current.change_script,
        query_spec=current.query_spec,
        failure_summary=current.failure_summary,
    )

    # 2. Shrink file contents line-by-line.
    current, used = _shrink_file_contents(current, predicate, max(0, max_evaluations - total_evals))
    total_evals += used

    # 3. Shrink change script ops if present.
    if current.change_script is not None and len(current.change_script.ops) > 1:
        def ops_fail(subset: list[ChangeOp]) -> bool:
            trial = FailingInstance(
                selector=current.selector,
                tree_spec=current.tree_spec,
                change_script=ChangeScript(selector=current.selector, ops=tuple(subset)),
                query_spec=current.query_spec,
                failure_summary=current.failure_summary,
            )
            return predicate(trial)

        shrunk_ops, used = ddmin(
            current.change_script.ops,
            ops_fail,
            max_evaluations=max(1, max_evaluations - total_evals),
        )
        total_evals += used
        current = FailingInstance(
            selector=current.selector,
            tree_spec=current.tree_spec,
            change_script=ChangeScript(selector=current.selector, ops=tuple(shrunk_ops)),
            query_spec=current.query_spec,
            failure_summary=current.failure_summary,
        )

    # 4. Simplify query filters if present.
    current, used = _shrink_query(current, predicate, max(0, max_evaluations - total_evals))
    total_evals += used

    return ShrinkResult(
        original_entry_count=orig_entries,
        shrunk_entry_count=len(current.tree_spec.entries),
        original_op_count=orig_ops,
        shrunk_op_count=len(current.change_script.ops) if current.change_script else 0,
        evaluations=total_evals,
        shrunk=current,
    )
