"""TB05 · Agent edits and the read basis (Case families: E01, E02, E03, E08, E10).

Implements:
  - D14 10-route edit catalog (4 native `command` operations, 2 callable tools,
    4 shell routes through `command`).
  - Complete fixed slot ledger definitions matching `ledger.md`:
      * E01: 62 sidecar slots (10 routes x 6 read forms + 2 combined read-and-write responses)
      * E02: 4 sidecar slots (+ 6 in-process slots in `lycaon/test/wea/tb05_edits_test.go`)
      * E03: 7 sidecar slots (person draft, external program, two windows, unimportable/conflict)
      * E08: 9 sidecar slots (UTF-8 BOM, UTF-16LE, CRLF/LF/mixed EOL, invalid UTF-8,
             long line, D17 sizes around 8 MiB + 64 MiB refusal, D6 synthetic canary span)
      * E10: 1 sidecar real-restart slot (+ 4 in-process commit-point restart slots in Go)
  - Deterministic, fail-closed evaluators verifying:
      * Only qualifying reads (`full_read`, `full_summarize`) authorize writes.
      * Non-qualifying/stale/truncated/missing reads refuse without mutating disk bytes.
      * Outside bytes are preserved exactly by SHA-256 hash.
      * Overlap asks for reread and changes nothing on disk.
      * Open-document state matches disk bytes after every slot.
      * One version per effect after every restart slot, no duplicate versions, no partial writes.
  - Real sidecar driver reusing TB02's `wea.runner`, `wea.observers`, `wea.canary`, and `wea.checker`.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import pathlib
import secrets
import socket
import sqlite3
import subprocess
import sys
import uuid
from dataclasses import dataclass
from typing import Any, Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.checker import check_evidence
from wea.observers import EditsObserver, hash_file, hash_tree, sha256_bytes
from wea.provenance import REPO_ROOT, collect_provenance, resolve_engine_binary
from wea.runner import (
    _create_application,
    _extract_refusal_code,
    _init_git_repo,
    _open_session,
    _resolve_engine_root,
    _save_json,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)

# ============================================================================
# D14 · The ten agent edit routes (and no others)
# ============================================================================

@dataclass(frozen=True)
class EditRoute:
    route_id: str
    kind: str  # "command_native" | "callable_tool" | "command_shell"
    tool_name: str  # Top-level callable tool: "command" | "code_rewrite" | "restore_version"
    operation: str  # D14 operation name
    target_file: str


D14_EDIT_ROUTES: tuple[EditRoute, ...] = (
    # Four native `command` operations (operation-schemas/, NOT top-level callable tools)
    EditRoute("write", "command_native", "command", "write", "target.txt"),
    EditRoute("edit", "command_native", "command", "edit", "target.txt"),
    EditRoute("replace_lines", "command_native", "command", "replace_lines", "target.txt"),
    EditRoute("jq_edit", "command_native", "command", "jq_edit", "target.json"),
    # Two callable tools (schemas/)
    EditRoute("code_rewrite", "callable_tool", "code_rewrite", "code_rewrite", "target.go"),
    EditRoute("restore_version", "callable_tool", "restore_version", "restore_version", "target.txt"),
    # Four shell routes through `command`
    EditRoute("shell_sed", "command_shell", "command", "sed", "target.txt"),
    EditRoute("shell_patch", "command_shell", "command", "patch", "target.txt"),
    EditRoute("shell_redirect", "command_shell", "command", "redirect", "target.txt"),
    EditRoute("shell_program", "command_shell", "command", "installed_program", "target.txt"),
)

# Six read forms for E01: only `full_read` and `full_summarize` qualify.
E01_READ_FORMS: tuple[dict[str, Any], ...] = (
    {"id": "full_read", "qualifies": True, "description": "Complete file read via native command `cat`"},
    {"id": "partial_window", "qualifies": False, "description": "Partial line window via `head -n 1`"},
    {"id": "outline", "qualifies": False, "description": "Structural outline only via `outline`"},
    {"id": "inline_input", "qualifies": False, "description": "Text pasted in user prompt with no host read"},
    {"id": "stale_disk", "qualifies": False, "description": "Full read followed by unseen external disk change"},
    {"id": "full_summarize", "qualifies": True, "description": "Exact full-document `summarize` source window"},
)

# Typed read-basis / stale-target refusal codes accepted for negative E01 variants.
# Any schema failure, unsupported operation, posture gate, or untyped error MUST fail closed.
E01_READ_BASIS_REFUSAL_CODES: frozenset[str] = frozenset({
    "EDITOR_DOCUMENT_READ_REQUIRED",
    "EDITOR_DOCUMENT_CHANGING",
    "EDIT_TARGET_CHANGED_BY_OTHERS",
    "TEXT_WRITE_CONFLICT",
    "WRITE_READ_REQUIRED",
    "READ_REQUIRED",
    "STALE_READ_BASIS",
})

# Compiled native / callable operations whose dispatcher operation identity must match D14.
D14_COMPILED_OPERATIONS: frozenset[str] = frozenset({
    "write",
    "edit",
    "replace_lines",
    "jq_edit",
    "code_rewrite",
    "restore_version",
})

D17_READ_BOUND_BYTES = 8 * 1024 * 1024  # 8 MiB (readcaps.MaxFileBytes)
D17_MUTATION_BOUND_BYTES = 4 * 1024 * 1024  # 4 MiB (readcaps.MaxMutationBytes)
D17_REFUSAL_SIZE_BYTES = 64 * 1024 * 1024  # 64 MiB


def build_tb05_slot_catalog() -> dict[str, list[dict[str, Any]]]:
    """Return the exact fixed TB05 slot catalog matching `ledger.md` (D18)."""
    e01_slots: list[dict[str, Any]] = []
    for route in D14_EDIT_ROUTES:
        for rf in E01_READ_FORMS:
            # Person-approved D14 Option A2 split (TB05-read-basis-person-approved.md):
            # the 6 document-aware structured edit routes require a whole conversational read;
            # the 4 program-effect shell routes (`command_shell`) preserve execution-pinned semantics.
            expected_auth = True if route.kind == "command_shell" else bool(rf["qualifies"])
            e01_slots.append({
                "slot_id": f"E01-{route.route_id}-{rf['id']}",
                "family": "E01",
                "lane": "sidecar",
                "route_id": route.route_id,
                "route_kind": route.kind,
                "tool_name": route.tool_name,
                "operation": route.operation,
                "read_form": rf["id"],
                "expected_authorized": expected_auth,
            })
    # Two combined read-and-write responses (60 + 2 = 62 E01 sidecar slots).
    e01_slots.append({
        "slot_id": "E01-combined-read-then-write-same-response",
        "family": "E01",
        "lane": "sidecar",
        "route_id": "edit",
        "route_kind": "command_native",
        "tool_name": "command",
        "operation": "edit",
        "read_form": "combined_same_response_unseen",
        "expected_authorized": False,
    })
    e01_slots.append({
        "slot_id": "E01-combined-write-then-edit-same-response",
        "family": "E01",
        "lane": "sidecar",
        "route_id": "edit",
        "route_kind": "command_native",
        "tool_name": "command",
        "operation": "edit",
        "read_form": "combined_same_response_own_write",
        "expected_authorized": True,
    })

    e02_slots: list[dict[str, Any]] = [
        {"slot_id": "E02-sidecar-non-overlapping-rebase", "family": "E02", "lane": "sidecar", "scenario": "non_overlapping_rebase"},
        {"slot_id": "E02-sidecar-overlapping-reread", "family": "E02", "lane": "sidecar", "scenario": "overlapping_reread"},
        {"slot_id": "E02-sidecar-retry-keeps-basis", "family": "E02", "lane": "sidecar", "scenario": "retry_keeps_basis"},
        {"slot_id": "E02-sidecar-merged-withdraws-basis", "family": "E02", "lane": "sidecar", "scenario": "merged_withdraws_basis"},
    ]

    e03_slots: list[dict[str, Any]] = [
        {"slot_id": "E03-sidecar-person-unsaved-draft", "family": "E03", "lane": "sidecar", "scenario": "person_unsaved_draft"},
        {"slot_id": "E03-sidecar-external-program-merge", "family": "E03", "lane": "sidecar", "scenario": "external_program_merge"},
        {"slot_id": "E03-sidecar-two-windows-converge", "family": "E03", "lane": "sidecar", "scenario": "two_windows_converge"},
        {"slot_id": "E03-sidecar-unimportable-comparison-view", "family": "E03", "lane": "sidecar", "scenario": "unimportable_comparison_view"},
        {"slot_id": "E03-sidecar-apply-stale-revision-refused", "family": "E03", "lane": "sidecar", "scenario": "apply_stale_revision_refused"},
        {"slot_id": "E03-sidecar-apply-stale-disk-hash-refused", "family": "E03", "lane": "sidecar", "scenario": "apply_stale_disk_hash_refused"},
        {"slot_id": "E03-sidecar-deleted-underneath-draft-recreate", "family": "E03", "lane": "sidecar", "scenario": "deleted_underneath_draft_recreate"},
    ]

    e08_slots: list[dict[str, Any]] = [
        {"slot_id": "E08-sidecar-mixed-eol", "family": "E08", "lane": "sidecar", "scenario": "mixed_eol"},
        {"slot_id": "E08-sidecar-utf8-bom-crlf", "family": "E08", "lane": "sidecar", "scenario": "utf8_bom_crlf"},
        {"slot_id": "E08-sidecar-utf16le-bom", "family": "E08", "lane": "sidecar", "scenario": "utf16le_bom"},
        {"slot_id": "E08-sidecar-invalid-utf8-and-binary", "family": "E08", "lane": "sidecar", "scenario": "invalid_utf8_and_binary"},
        {"slot_id": "E08-sidecar-very-long-line", "family": "E08", "lane": "sidecar", "scenario": "very_long_line"},
        {"slot_id": "E08-sidecar-size-under-8mib", "family": "E08", "lane": "sidecar", "scenario": "size_under_8mib"},
        {"slot_id": "E08-sidecar-size-at-8mib", "family": "E08", "lane": "sidecar", "scenario": "size_at_8mib"},
        {"slot_id": "E08-sidecar-size-over-8mib-and-64mib-refusal", "family": "E08", "lane": "sidecar", "scenario": "size_over_8mib_and_64mib_refusal"},
        {"slot_id": "E08-sidecar-secret-canary-span", "family": "E08", "lane": "sidecar", "scenario": "secret_canary_span"},
    ]

    e10_slots: list[dict[str, Any]] = [
        {"slot_id": "E10-sidecar-real-restart-edit-durability", "family": "E10", "lane": "sidecar", "scenario": "real_restart_edit_durability"},
    ]

    return {
        "E01": e01_slots,
        "E02": e02_slots,
        "E03": e03_slots,
        "E08": e08_slots,
        "E10": e10_slots,
    }


# ============================================================================
# Deterministic fail-closed evaluators for E01, E02, E03, E08, E10
# ============================================================================

def evaluate_e01_slot(record: dict[str, Any]) -> dict[str, Any]:
    """Fail-closed evaluator for an E01 read-basis matrix slot.

    Enforces:
      - Unobserved cases (missing hashes, outcomes, read verification, or positive expected
        outcome) yield `verdict="unknown"`, never `"pass"`.
      - Read form completion is verified for host-read forms, inline-input absence of host
        reads is verified for `inline_input`, and single-response batch ordering is verified
        for combined read+write variants.
      - Compiled D14 routes (`write`, `edit`, `replace_lines`, `jq_edit`, `code_rewrite`,
        `restore_version`) must record and match their actual dispatcher operation.
      - Positive variants verify exact disk SHA-256 (or parsed JSON value for `jq_edit`)
        and exact outside-segment SHA-256 preservation.
      - Negative variants require a specific typed read-basis refusal code in
        `E01_READ_BASIS_REFUSAL_CODES` (rejecting schema failures, unsupported operations,
        posture gates, or untyped errors) AND unchanged disk bytes.
    """
    failures: list[dict[str, Any]] = []
    missing: list[str] = []
    expected_auth = bool(record.get("expected_authorized"))
    read_form = str(record.get("read_form") or "")
    route_id = str(record.get("route_id") or "")
    pre_sha = str(record.get("pre_disk_sha256") or "")
    post_sha = str(record.get("post_disk_sha256") or "")
    expected_post_sha = str(record.get("expected_post_sha256") or "")
    expected_json = record.get("expected_json_value")
    post_json = record.get("post_json_value")
    outcome = str(record.get("write_outcome") or "")
    refusal_code = str(record.get("refusal_code") or "")

    if not pre_sha:
        missing.append("pre_disk_sha256")
    if not post_sha:
        missing.append("post_disk_sha256")
    if not outcome or outcome == "unknown":
        missing.append("write_outcome")
    if expected_auth and not expected_post_sha and expected_json is None:
        missing.append("expected_post_sha256_or_expected_json_value")

    # Verify read-form instrumentation completeness before evaluating rules.
    if read_form in {"full_read", "partial_window", "outline", "stale_disk", "full_summarize"}:
        if not record.get("read_outcome"):
            missing.append("read_outcome")
    elif read_form == "inline_input":
        if "inline_input_verified" not in record or "read_call_issued" not in record:
            missing.append("inline_input_verified/read_call_issued")
    elif read_form in {"combined_same_response_unseen", "combined_same_response_own_write"}:
        if "same_response_ordering_verified" not in record or "same_response_batch_count" not in record:
            missing.append("same_response_ordering_verified/same_response_batch_count")

    # Verify dispatcher operation observation when configured on a compiled D14 route.
    expected_op = str(record.get("expected_operation") or "")
    dispatched_ops = record.get("dispatched_operations")
    if expected_op in D14_COMPILED_OPERATIONS and dispatched_ops is None:
        missing.append("dispatched_operations")

    if missing:
        return {
            "verdict": "unknown",
            "passed": False,
            "failures": [],
            "failed_rule_ids": [],
            "missing": missing,
        }

    # 1. Read-form completion / inline-input / combined-response ordering checks.
    if read_form in {"full_read", "partial_window", "outline", "stale_disk", "full_summarize"}:
        if str(record.get("read_outcome")) != "completed":
            failures.append({
                "rule_id": "E01_READ_FORM_NOT_COMPLETED",
                "message": f"Read form '{read_form}' did not complete (outcome={record.get('read_outcome')!r}).",
            })
    elif read_form == "inline_input":
        if record.get("inline_input_verified") is not True or record.get("read_call_issued") is not False:
            failures.append({
                "rule_id": "E01_INLINE_INPUT_NOT_VERIFIED",
                "message": "Inline-input variant failed independent verification (must carry inline prompt text and zero host read calls).",
            })
    elif read_form in {"combined_same_response_unseen", "combined_same_response_own_write"}:
        if record.get("same_response_ordering_verified") is not True or int(record.get("same_response_batch_count") or 0) < 2:
            failures.append({
                "rule_id": "E01_COMBINED_RESPONSE_ORDERING_INVALID",
                "message": "Combined read+write response did not independently verify >=2 ordered tool calls in one response.",
            })

    # 2. Actual dispatcher operation check for compiled D14 routes.
    if expected_op in D14_COMPILED_OPERATIONS and isinstance(dispatched_ops, list):
        if expected_op not in dispatched_ops:
            failures.append({
                "rule_id": "E01_DISPATCH_OPERATION_MISMATCH",
                "message": f"Route '{route_id}' expected compiled operation '{expected_op}', observed {dispatched_ops}.",
                "details": {"expected_operation": expected_op, "dispatched_operations": dispatched_ops},
            })

    # 3. Positive vs. negative authorization outcome checks.
    if expected_auth:
        if outcome != "completed":
            failures.append({
                "rule_id": "E01_QUALIFYING_READ_REFUSED",
                "message": f"Qualifying read '{read_form}' on route '{route_id}' was refused ({outcome}, code={refusal_code}).",
            })
        if expected_post_sha and post_sha != expected_post_sha:
            failures.append({
                "rule_id": "E01_POST_BYTES_MISMATCH",
                "message": "Authorized write produced unexpected disk SHA-256.",
                "details": {"expected": expected_post_sha, "actual": post_sha},
            })
        if expected_json is not None and post_json != expected_json:
            failures.append({
                "rule_id": "E01_POST_JSON_MISMATCH",
                "message": "Authorized structured edit produced unexpected parsed JSON value.",
                "details": {"expected": expected_json, "actual": post_json},
            })
        outside_pre = record.get("outside_segment_pre_sha256")
        outside_post = record.get("outside_segment_post_sha256")
        if outside_pre and outside_post and outside_pre != outside_post:
            failures.append({
                "rule_id": "E01_OUTSIDE_BYTES_MUTATED",
                "message": "Bytes outside the edited span changed.",
                "details": {"pre": outside_pre, "post": outside_post},
            })
    else:
        if outcome == "completed":
            failures.append({
                "rule_id": "E01_NON_QUALIFYING_READ_AUTHORIZED",
                "message": f"Non-qualifying read '{read_form}' on route '{route_id}' authorized a write.",
            })
        if post_sha != pre_sha:
            failures.append({
                "rule_id": "E01_REFUSED_WRITE_MUTATED_DISK",
                "message": "Refused write mutated disk bytes.",
                "details": {"pre_sha256": pre_sha, "post_sha256": post_sha},
            })
        if not refusal_code:
            failures.append({
                "rule_id": "E01_MISSING_READ_BASIS_REFUSAL_CODE",
                "message": f"Negative variant '{read_form}' on '{route_id}' produced no typed read-basis refusal code.",
            })
        elif refusal_code not in E01_READ_BASIS_REFUSAL_CODES:
            failures.append({
                "rule_id": "E01_WRONG_REFUSAL_CLASS",
                "message": f"Negative variant '{read_form}' on '{route_id}' failed with '{refusal_code}', not a read-basis refusal code.",
                "details": {"refusal_code": refusal_code, "expected_codes": sorted(E01_READ_BASIS_REFUSAL_CODES)},
            })

    return {
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": [f["rule_id"] for f in failures],
    }


def evaluate_e02_slot(record: dict[str, Any]) -> dict[str, Any]:
    """Fail-closed evaluator for E02 rebase, overlap, retry, and basis-withdrawal slots."""
    failures: list[dict[str, Any]] = []
    scenario = str(record.get("scenario") or "")
    pre_sha = str(record.get("pre_disk_sha256") or "")
    post_sha = str(record.get("post_disk_sha256") or "")
    if not scenario or not pre_sha or not post_sha:
        return {"verdict": "unknown", "passed": False, "failures": [], "missing": ["scenario/pre_disk_sha256/post_disk_sha256"]}

    if scenario == "overlapping_reread":
        if record.get("write_outcome") == "completed":
            failures.append({
                "rule_id": "E02_OVERLAP_SILENTLY_ACCEPTED",
                "message": "Overlapping edit succeeded instead of requesting a reread.",
            })
        if post_sha != pre_sha:
            failures.append({
                "rule_id": "E02_OVERLAP_MUTATED_DISK",
                "message": "Overlapping edit modified disk bytes.",
                "details": {"pre_sha256": pre_sha, "post_sha256": post_sha},
            })
    elif scenario == "non_overlapping_rebase":
        expected_combined = str(record.get("expected_combined_sha256") or "")
        if not expected_combined:
            return {"verdict": "unknown", "passed": False, "failures": [], "missing": ["expected_combined_sha256"]}
        if post_sha != expected_combined:
            failures.append({
                "rule_id": "E02_REBASE_BYTES_MISMATCH",
                "message": "Non-overlapping rebase did not preserve both edits.",
                "details": {"expected": expected_combined, "actual": post_sha},
            })
        if record.get("outside_segment_pre_sha256") != record.get("outside_segment_post_sha256"):
            failures.append({
                "rule_id": "E02_OUTSIDE_BYTES_CORRUPTED",
                "message": "Untouched bytes outside both edits changed during rebase.",
            })
    elif scenario == "retry_keeps_basis":
        if record.get("retry_basis_revision") != record.get("initial_basis_revision"):
            failures.append({
                "rule_id": "E02_RETRY_CHANGED_BASIS",
                "message": "Retry silently refreshed the read basis instead of keeping the original basis.",
            })
    elif scenario == "merged_withdraws_basis":
        if record.get("followup_without_reread_outcome") == "completed":
            failures.append({
                "rule_id": "E02_MERGED_BASIS_NOT_WITHDRAWN",
                "message": "Write after merged result containing another participant's text succeeded without reread.",
            })

    return {
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": [f["rule_id"] for f in failures],
    }


def evaluate_e03_slot(record: dict[str, Any]) -> dict[str, Any]:
    """Fail-closed evaluator for E03 open-document vs disk consistency slots."""
    failures: list[dict[str, Any]] = []
    disk_sha = str(record.get("post_disk_sha256") or "")
    doc_sha = str(record.get("open_document_sha256") or "")
    if not disk_sha or not doc_sha:
        return {"verdict": "unknown", "passed": False, "failures": [], "missing": ["post_disk_sha256/open_document_sha256"]}

    expect_diverged = bool(record.get("expect_diverged", False))
    if expect_diverged:
        if not record.get("document_diverged"):
            failures.append({
                "rule_id": "E03_UNIMPORTABLE_NOT_MARKED_DIVERGED",
                "message": "Unimportable outside file did not enter diverged/comparison state.",
            })
        pre_disk_sha = str(record.get("pre_disk_sha256") or "")
        if pre_disk_sha and disk_sha != pre_disk_sha:
            failures.append({
                "rule_id": "E03_DIVERGED_CLOBBERED_DISK",
                "message": "Diverged document operation clobbered disk bytes.",
            })
    else:
        if disk_sha != doc_sha:
            failures.append({
                "rule_id": "E03_DOCUMENT_DISK_DRIFT",
                "message": "Document bytes seen by the person do not match bytes on disk after slot.",
                "details": {"document_sha256": doc_sha, "disk_sha256": disk_sha},
            })

    if record.get("expect_refused") and record.get("apply_outcome") == "completed":
        failures.append({
            "rule_id": "E03_STALE_APPLY_ACCEPTED",
            "message": "Comparison apply accepted a stale revision or stale disk hash.",
        })

    return {
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": [f["rule_id"] for f in failures],
    }


def evaluate_e08_slot(record: dict[str, Any]) -> dict[str, Any]:
    """Fail-closed evaluator for E08 encodings, line endings, sizes (D17), and D6 canary spans."""
    failures: list[dict[str, Any]] = []
    pre_sha = str(record.get("pre_disk_sha256") or "")
    post_sha = str(record.get("post_disk_sha256") or "")
    if not pre_sha or not post_sha:
        return {"verdict": "unknown", "passed": False, "failures": [], "missing": ["pre_disk_sha256/post_disk_sha256"]}

    if record.get("expect_refused"):
        if record.get("write_outcome") == "completed":
            failures.append({
                "rule_id": "E08_UNSUPPORTED_OR_OVERSIZE_ACCEPTED",
                "message": f"Unsupported/oversize edit '{record.get('scenario')}' was not refused.",
            })
        if post_sha != pre_sha:
            failures.append({
                "rule_id": "E08_REFUSED_MUTATED_DISK",
                "message": "Refused E08 edit mutated disk bytes.",
                "details": {"pre_sha256": pre_sha, "post_sha256": post_sha},
            })
    else:
        expected_post = str(record.get("expected_post_sha256") or "")
        if expected_post and post_sha != expected_post:
            failures.append({
                "rule_id": "E08_ENCODING_OR_EOL_MISMATCH",
                "message": "Post-edit bytes do not match exact expected encoding/BOM/EOL SHA-256.",
                "details": {"expected": expected_post, "actual": post_sha},
            })
        if record.get("outside_segment_pre_sha256") != record.get("outside_segment_post_sha256"):
            failures.append({
                "rule_id": "E08_OUTSIDE_BYTES_MUTATED",
                "message": "Bytes outside the edited span were altered.",
            })

    if record.get("scenario") == "secret_canary_span":
        canary_scan = record.get("canary_scan")
        if not isinstance(canary_scan, dict):
            return {"verdict": "unknown", "passed": False, "failures": [], "missing": ["canary_scan"]}
        if canary_scan.get("clean") is False or int(canary_scan.get("leak_count", 0)) > 0:
            failures.append({
                "rule_id": "E08_CANARY_LEAKED_TO_RECORDS",
                "message": "Synthetic D6 canary leaked into screened view or persisted records.",
            })

    return {
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": [f["rule_id"] for f in failures],
    }


def evaluate_e10_slot(record: dict[str, Any]) -> dict[str, Any]:
    """Fail-closed evaluator for E10 restart durability across edit commit points."""
    failures: list[dict[str, Any]] = []
    disk_sha = str(record.get("post_restart_disk_sha256") or "")
    tip_sha = str(record.get("ledger_tip_sha256") or "")
    expected_sha = str(record.get("expected_post_sha256") or "")
    effect_count = record.get("effect_count")
    version_count = record.get("version_count")
    expected_effects = record.get("expected_effect_count")

    if not disk_sha or not tip_sha or effect_count is None or version_count is None or expected_effects is None:
        return {
            "verdict": "unknown",
            "passed": False,
            "failures": [],
            "missing": ["post_restart_disk_sha256/ledger_tip_sha256/effect_count/version_count/expected_effect_count"],
        }

    if expected_sha and disk_sha != expected_sha:
        failures.append({
            "rule_id": "E10_PARTIAL_OR_CORRUPT_WRITE",
            "message": "Post-restart disk bytes do not match expected committed edit.",
            "details": {"expected": expected_sha, "actual": disk_sha},
        })
    if disk_sha != tip_sha:
        failures.append({
            "rule_id": "E10_DISK_LEDGER_TIP_MISMATCH",
            "message": "Post-restart disk bytes do not match source ledger tip.",
            "details": {"disk_sha256": disk_sha, "ledger_tip_sha256": tip_sha},
        })
    if int(effect_count) != int(expected_effects):
        failures.append({
            "rule_id": "E10_EFFECT_COUNT_MISMATCH",
            "message": f"Expected {expected_effects} effect(s) after restart, found {effect_count}.",
        })
    if int(version_count) < int(expected_effects) or int(version_count) > int(expected_effects) + 1:
        # Initial tracked baseline + 1 version per effect (no duplicate versions for the same effect).
        failures.append({
            "rule_id": "E10_DUPLICATE_OR_MISSING_VERSION",
            "message": f"Unexpected version count {version_count} for {expected_effects} effect(s) after restart.",
        })
    if record.get("open_document_sha256") and record.get("open_document_sha256") != disk_sha:
        failures.append({
            "rule_id": "E10_POST_RESTART_DOC_DISK_DRIFT",
            "message": "Post-restart open document does not match disk bytes.",
        })

    return {
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": [f["rule_id"] for f in failures],
    }


# ============================================================================
# Sidecar command builders for D14 routes & E01 read forms
# ============================================================================

def build_read_call(read_form: str, target_file: str) -> tuple[str, dict[str, Any]] | None:
    """Build the `(tool_name, args)` call for an E01 read form, or None for inline_input."""
    if read_form in {"full_read", "stale_disk"}:
        return ("command", {"command": f"cat {target_file}"})
    if read_form == "partial_window":
        return ("command", {"command": f"head -n 5 {target_file}"})
    if read_form == "outline":
        return ("command", {"command": f"outline --path {target_file}"})
    if read_form == "full_summarize":
        return ("summarize", {"task": f"Read full contents of {target_file}", "paths": [target_file]})
    if read_form == "inline_input":
        return None
    raise ValueError(f"Unknown E01 read form: {read_form}")


def build_route_write_call(
    route: EditRoute,
    *,
    restore_version_id: str = "",
    target_line: int = 2,
    after_text: str = "alpha\nBETA\ngamma\n",
    patch_body: str = "",
) -> tuple[str, dict[str, Any]]:
    """Build the `(tool_name, args)` call for each of the ten D14 edit routes."""
    if route.route_id == "write":
        # Compiled native `write` operation via `tee` heredoc (dispositions.json: tool="write",
        # command_forms=["tee", "ln"], no printf pipeline stage).
        body = after_text.rstrip("\n")
        return ("command", {"command": f"tee {route.target_file} <<'EOF'\n{body}\nEOF"})
    if route.route_id == "edit":
        return ("command", {"command": f'edit --path {route.target_file} --old-string "beta" --new-string "BETA"'})
    if route.route_id == "replace_lines":
        return ("command", {"command": f'replace-lines --path {route.target_file} --start {target_line} --end {target_line} --content "BETA"'})
    if route.route_id == "jq_edit":
        return ("command", {"command": f'jq-edit --path {route.target_file} --query \'.value = "BETA"\''})
    if route.route_id == "code_rewrite":
        return ("code_rewrite", {
            "path": route.target_file,
            "pattern": "return 1 + 2",
            "rewrite": "return 3",
        })
    if route.route_id == "restore_version":
        args: dict[str, Any] = {"path": route.target_file}
        if restore_version_id:
            args["version_id"] = restore_version_id
        return ("restore_version", args)
    if route.route_id == "shell_sed":
        return ("command", {"command": f"sed -i 's/beta/BETA/' {route.target_file}"})
    if route.route_id == "shell_patch":
        if not patch_body:
            patch_body = "--- a/target.txt\n+++ b/target.txt\n@@ -1,3 +1,3 @@\n alpha\n-beta\n+BETA\n gamma\n"
        return ("command", {"command": f"patch -p1 <<'EOF'\n{patch_body.rstrip()}\nEOF"})
    if route.route_id == "shell_redirect":
        body = after_text.rstrip("\n")
        return ("command", {"command": f"cat <<'EOF' > {route.target_file}\n{body}\nEOF"})
    if route.route_id == "shell_program":
        return ("command", {
            "command": f'python3 -c \'from pathlib import Path; p = Path("{route.target_file}"); p.write_text(p.read_text().replace("beta", "BETA"))\''
        })
    raise ValueError(f"Unknown D14 route: {route.route_id}")


# ============================================================================
# Sidecar runner for TB05 E10 real-restart slot (and E01/E02/E03/E08 slots)
# ============================================================================

def _restart_application(
    app_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
) -> Any:
    """Restart a real `lycaon-dev` sidecar on an EXISTING isolated profile directory (`app_dir`).

    Preserves `app_dir/store.db` and `app_dir/config/`, appends to `app_dir/sidecar.log`,
    binds a fresh ephemeral localhost port and bearer token, and launches the pinned binary.
    """
    import smoke

    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
    resolved_engine_root = _resolve_engine_root(engine_manifest=engine_manifest, engine_root=engine_root)
    app = smoke.Application.__new__(smoke.Application)
    app.directory = pathlib.Path(app_dir).resolve()
    app.config = app.directory / "config"
    app.config.mkdir(parents=True, exist_ok=True)
    app.manual = True
    app.projects = []
    app.admissions = []
    suite = json.loads((REPO_ROOT / "lycaon/test/fixtures/eval/coordinator-benchmark.json").read_text(encoding="utf-8"))
    app.task_allowance = suite["task_allowance"]
    app.approval_guidance = suite["unattended"]["approval_guidance"]
    app.baseline_checks = []
    app.flows = []
    app.token = secrets.token_hex(24)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        app.port = sock.getsockname()[1]
    app.url = f"http://127.0.0.1:{app.port}"
    app.log = (app.directory / "sidecar.log").open("a", encoding="utf-8")
    env_overrides = {
        "LYCAON_CONFIG_DIR": str(app.config),
        "LYCAON_CONFIG_ROOT": str(REPO_ROOT / "lycaon"),
        "LYCAON_ENGINE_ROOT": str(resolved_engine_root or (REPO_ROOT / "lycaon-den/src-tauri/engine-root")),
        "LYCAON_ADDR": f"127.0.0.1:{app.port}",
        "LYCAON_API_TOKEN": app.token,
        "LYCAON_DEV": "1",
        "LYCAON_HARNESS": "1",
        "LYCAON_LOG_LEVEL": "debug",
        "LYCAON_LLM_MOCK": "1",
        "LYCAON_LLM_MANUAL": "1",
    }
    env = smoke.application_environment(env_overrides)
    app.process = subprocess.Popen(
        [str(binary), "serve", "--db", str(app.directory / "store.db")],
        cwd=REPO_ROOT,
        env=env,
        stdout=app.log,
        stderr=subprocess.STDOUT,
    )
    _save_json(app.directory / "ownership.json", {"pid": app.process.pid, "port": app.port})
    return app


def read_open_document_sha256_ro(
    db_path: pathlib.Path,
    *,
    doc_id: str = "",
    rel_path: str = "",
    doc_resp: dict[str, Any] | None = None,
) -> str:
    """Compute the exact serialized SHA-256 of the open editor document from SQLite (`?mode=ro`) or DTO."""
    if db_path.is_file() and (doc_id or rel_path):
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            conn.row_factory = sqlite3.Row
            tables = {
                r[0]
                for r in conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                ).fetchall()
            }
            if "editor_documents" in tables:
                if doc_id:
                    row = conn.execute(
                        "SELECT draft, eol, base_sha256, dirty FROM editor_documents WHERE id = ? LIMIT 1",
                        (doc_id,),
                    ).fetchone()
                else:
                    row = conn.execute(
                        "SELECT draft, eol, base_sha256, dirty FROM editor_documents WHERE path = ? ORDER BY rowid DESC LIMIT 1",
                        (rel_path,),
                    ).fetchone()
                if row:
                    if not row["dirty"] and row["base_sha256"]:
                        return str(row["base_sha256"])
                    draft = row["draft"]
                    if isinstance(draft, str):
                        eol = str(row["eol"] or "lf").lower()
                        serialized = draft.replace("\r\n", "\n").replace("\n", "\r\n") if eol == "crlf" else draft
                        return sha256_bytes(serialized.encode("utf-8"))
    if isinstance(doc_resp, dict):
        if not doc_resp.get("dirty") and doc_resp.get("base_sha256"):
            return str(doc_resp["base_sha256"])
        if isinstance(doc_resp.get("draft"), str):
            return sha256_bytes(doc_resp["draft"].encode("utf-8"))
        if isinstance(doc_resp.get("base_content"), str):
            return sha256_bytes(doc_resp["base_content"].encode("utf-8"))
    return ""


def query_file_durability_ro(db_path: pathlib.Path, rel_path: str) -> tuple[int, int, str]:
    """Query `(effect_count, version_count, tip_sha256)` for `rel_path` via `?mode=ro` using the real schema."""
    effect_count = 0
    version_count = 0
    tip_sha = ""
    if not db_path.is_file():
        return effect_count, version_count, tip_sha
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        v_row = conn.execute(
            "SELECT file_id, content_sha256 FROM source_versions "
            "WHERE path = ? AND branch_id = '' ORDER BY seq DESC LIMIT 1",
            (rel_path,),
        ).fetchone()
        if v_row:
            fid = str(v_row["file_id"])
            if v_row["content_sha256"]:
                tip_sha = str(v_row["content_sha256"])
            effect_count = int(
                conn.execute("SELECT COUNT(1) FROM source_effects WHERE file_id=?", (fid,)).fetchone()[0]
            )
            version_count = int(
                conn.execute("SELECT COUNT(1) FROM source_versions WHERE file_id=?", (fid,)).fetchone()[0]
            )
    return effect_count, version_count, tip_sha


def run_e10_sidecar_restart_slot(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Confirm one edit path across a real sidecar engine restart (TB05 / D3 / E10)."""
    run_id = f"tb05-e10-restart-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("e10")
    proj = out_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    initial = "line1\nbeta\nline3\n"
    expected_after = "line1\nBETA\nline3\n"
    expected_sha = sha256_bytes(expected_after.encode("utf-8"))
    (proj / "restart_target.txt").write_text(initial, encoding="utf-8")
    _init_git_repo(proj)

    app_dir = out_dir / "app"
    canaries.materialize_env_file(out_dir / "isolated-canaries", {"WEA_TB05_CANARY": canary_val})
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")

    # Phase 1: Start real engine, read and edit restart_target.txt, then stop cleanly.
    app1 = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
    session_id = ""
    project_id = ""
    root_id = ""
    write_res: dict[str, Any] = {}
    try:
        app1.ready()
        session_id, project_id, root_id = _open_session(app1, proj)
        app1.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Read restart_target.txt and change beta to BETA."},
        )
        _send_tool_call(app1, session_id, "update_progress", {"content": "## Progress\n- [ ] Edit restart_target.txt\n"})
        _send_tool_call(app1, session_id, "command", {"command": "cat restart_target.txt"})
        _, write_res, _ = _send_tool_call(
            app1, session_id, "command",
            {"command": "edit --path restart_target.txt --old-string beta --new-string BETA"},
            checkpoint_policy="approve_chat",
        )
        app1.request(f"/v1/sessions/{session_id}/abort", {})
    finally:
        app1.close(cleanup=False)

    # Phase 2: Real sidecar restart on the exact same profile directory (`app_dir`).
    doc_resp: dict[str, Any] = {}
    app2 = _restart_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
    try:
        app2.ready()
        with contextlib.suppress(Exception):
            doc_resp = app2.request(
                f"/v1/projects/{project_id}/editor-documents",
                {"path": "restart_target.txt", "root_id": root_id, "client_id": "wea-tb05-restart-observer"},
            )
    finally:
        app2.close(cleanup=False)

    db_path = app_dir / "store.db"
    open_doc_sha = read_open_document_sha256_ro(
        db_path,
        doc_id=str(doc_resp.get("id") or "") if isinstance(doc_resp, dict) else "",
        rel_path="restart_target.txt",
        doc_resp=doc_resp if isinstance(doc_resp, dict) else None,
    )
    post_disk_sha = str(hash_file(proj / "restart_target.txt").get("sha256") or "")
    db_path = app_dir / "store.db"
    effect_count, version_count, tip_sha = query_file_durability_ro(db_path, "restart_target.txt")

    record = {
        "slot_id": "E10-sidecar-real-restart-edit-durability",
        "run_id": run_id,
        "provenance": prov,
        "expected_post_sha256": expected_sha,
        "post_restart_disk_sha256": post_disk_sha,
        "ledger_tip_sha256": tip_sha,
        "open_document_sha256": open_doc_sha,
        "effect_count": effect_count,
        "version_count": version_count,
        "expected_effect_count": 1,
        "write_outcome": str((write_res or {}).get("outcome") or ""),
        "canary_scan": scan_for_canaries(canaries.tokens, sqlite_dbs=[db_path], paths=[app_dir / "sidecar.log"]),
    }
    verdict = evaluate_e10_slot(record)
    receipt = {"record": record, "verdict": verdict}
    _save_json(out_dir / "e10_sidecar_restart_receipt.json", receipt)
    return receipt


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="tb05_edits", description="TB05 Agent edits and the read basis harness.")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("catalog", help="Print the fixed TB05 slot catalog and counts.")
    p_e10 = sub.add_parser("run-e10-restart", help="Run the E10 sidecar real-restart durability slot.")
    p_e10.add_argument("--out", type=pathlib.Path, required=True)
    p_e10.add_argument("--engine", type=pathlib.Path, default=None)
    p_e10.add_argument("--engine-manifest", type=pathlib.Path, default=None)
    p_e10.add_argument("--engine-root", type=pathlib.Path, default=None)

    args = parser.parse_args(argv)
    if args.cmd == "catalog":
        cat = build_tb05_slot_catalog()
        summary = {k: len(v) for k, v in cat.items()}
        print(json.dumps({"counts": summary, "total_sidecar_slots": sum(summary.values()), "catalog": cat}, indent=2))
        return 0
    if args.cmd == "run-e10-restart":
        res = run_e10_sidecar_restart_slot(
            args.out,
            engine_bin=args.engine,
            engine_manifest=args.engine_manifest,
            engine_root=args.engine_root,
        )
        print(json.dumps(res["verdict"], indent=2))
        return 0 if res["verdict"]["passed"] else 1
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
