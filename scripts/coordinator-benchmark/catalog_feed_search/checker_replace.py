"""D4 Differential Checker for replacement apply outcomes (R02/R03, Decisions 009/012)."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass

from .checker_types import CheckResult, DeclaredGap, Verdict, evaluate_omissions
from .references import ReplaceFileResult


@dataclass(frozen=True)
class ReplaceFileObservation:
    """Observed product state for one file after replace apply."""

    path: str
    root_id: str = "root-0"
    applied: bool = True
    refused_drift: bool = False
    observed_sha256: str = ""
    expected_before_sha256: str = ""
    drifted_before_apply: bool = False
    drifted_sha256: str | None = None


@dataclass(frozen=True)
class ProductReplaceAnswer:
    """Product replace-apply outcome with declared gaps."""

    files: tuple[ReplaceFileObservation, ...]
    declared_gaps: tuple[DeclaredGap, ...] = ()
    instrumentation_complete: bool = True


def _normalize_replace_reference(
    reference_results: dict[str, ReplaceFileResult] | dict[tuple[str, str], ReplaceFileResult],
) -> dict[tuple[str, str], ReplaceFileResult]:
    out: dict[tuple[str, str], ReplaceFileResult] = {}
    for key, val in reference_results.items():
        if isinstance(key, tuple):
            out[(key[0], key[1])] = val
        else:
            out[(val.root_id, key)] = val
    return out


def check_replace(
    reference_results: dict[str, ReplaceFileResult] | dict[tuple[str, str], ReplaceFileResult],
    answer: ProductReplaceAnswer,
) -> CheckResult:
    """Compare product replace outcomes against reference replacement keyed by (root_id, path)."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "replace", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    ref_map = _normalize_replace_reference(reference_results)

    obs_counts = Counter((f.root_id, f.path) for f in answer.files)
    wrong: list[str] = [
        f"duplicate_replace_observation:{r}:{p} observed={c}"
        for (r, p), c in sorted(obs_counts.items())
        if c > 1
    ]

    obs_by_key = {(f.root_id, f.path): f for f in answer.files}
    extra = sorted(f"{r}:{p}" for (r, p) in obs_by_key if (r, p) not in ref_map)
    missing_paths: list[tuple[str, str]] = []

    for (root_id, path), obs in sorted(obs_by_key.items()):
        ref = ref_map.get((root_id, path))
        if ref is None:
            continue
        if obs.drifted_before_apply:
            # Independent pre-apply drifted digest (never trust product-supplied expected_before_sha256).
            expected_unchanged = obs.drifted_sha256 if obs.drifted_sha256 is not None else ref.before_sha256
            if obs.applied or not obs.refused_drift:
                wrong.append(f"overwritten_drifted_file:{root_id}:{path}")
            elif obs.observed_sha256 != expected_unchanged:
                wrong.append(f"drifted_file_not_byte_identical:{root_id}:{path}")
        else:
            if obs.applied:
                if obs.observed_sha256 != ref.after_sha256:
                    wrong.append(f"replace_hash_mismatch:{root_id}:{path}")
            else:
                # Decision 012: an unapplied non-drifted file MUST remain byte-identical to
                # the independent reference before_sha256 even when excused by limited_preview.
                if obs.observed_sha256 != ref.before_sha256:
                    wrong.append(f"unapplied_file_bytes_changed:{root_id}:{path}")
                else:
                    missing_paths.append((root_id, path))

    for root_id, path in sorted(ref_map):
        if (root_id, path) not in obs_by_key:
            missing_paths.append((root_id, path))

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "replace",
            extra_claims=tuple(extra),
            wrong_claims=tuple(wrong),
            declared_gaps=declared_names,
            reason="replace mutated an extra/duplicate/drifted/unapplied file or produced wrong bytes",
        )

    if not missing_paths:
        return CheckResult(Verdict.PASS, "replace", declared_gaps=declared_names)

    return evaluate_omissions(
        domain="replace",
        missing_items=[(r, None, p, f"{r}:{p}") for r, p in missing_paths],
        declared_gaps=answer.declared_gaps,
    )
