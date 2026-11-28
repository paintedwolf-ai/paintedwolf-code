"""TB06 · Attribution, restore and rewind harness, observers, and evaluators (E04, E05, E06, E07, E09).

Provides:
  - Read-only SQLite (`?mode=ro`) + SHA-256 filesystem observers for TB06 state.
  - Deterministic, fail-closed evaluators for E04, E05, E06, E07, and E09.
  - Go in-process lane runner and `WEA_TB06_RECEIPT:` parser for `lycaon/test/wea/tb06_history_test.go`.
  - Self-contained qualification controls (healthy, injected-defect, missing-instrumentation).
"""

from __future__ import annotations

import contextlib
import json
import pathlib
import sqlite3
import subprocess
from typing import Any

from wea.observers import hash_tree, sha256_bytes

RECEIPT_PREFIX = "WEA_TB06_RECEIPT:"

REWIND_TRACKED_TABLES = (
    "messages",
    "source_operations",
    "source_versions",
    "source_effects",
    "rewind_operations",
)

REQUIRED_E04_AUTHOR_KINDS = (
    "person",
    "agent",
    "worker",
    "landed_worker",
    "command",
    "external",
)

REQUIRED_E05_RESTORE_KINDS = (
    "text",
    "binary",
    "utf16",
    "rename",
    "delete_recreate",
    "git_version",
)


def _connect_ro(db_path: str | pathlib.Path) -> sqlite3.Connection:
    path = pathlib.Path(db_path).resolve()
    return sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)


def observe_attribution_state(db_path: str | pathlib.Path, project_id: str) -> dict[str, Any]:
    """Independently read source_operations, source_versions, source_effects, and source_line_attr via `?mode=ro`."""
    path = pathlib.Path(db_path).resolve()
    if not path.is_file():
        return {"available": False, "db_path": str(path)}
    with contextlib.closing(_connect_ro(path)) as conn:
        conn.row_factory = sqlite3.Row
        existing = {
            r[0]
            for r in conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            ).fetchall()
        }
        ops = [
            dict(r)
            for r in conn.execute(
                "SELECT id, branch_id, origin, person_id, session_id, job_id, turn, cause, command_window_id "
                "FROM source_operations WHERE project_id = ? ORDER BY rowid ASC",
                (project_id,),
            ).fetchall()
        ] if "source_operations" in existing else []
        versions = [
            dict(r)
            for r in conn.execute(
                "SELECT id, file_id, root_id, path, state, content_sha256, operation_id, seq, landing "
                "FROM source_versions WHERE project_id = ? ORDER BY seq ASC",
                (project_id,),
            ).fetchall()
        ] if "source_versions" in existing else []
        line_attr = [
            dict(r)
            for r in conn.execute(
                "SELECT branch_id, file_id, start_line, end_line, effect_id "
                "FROM source_line_attr WHERE project_id = ? ORDER BY branch_id, file_id, start_line ASC",
                (project_id,),
            ).fetchall()
        ] if "source_line_attr" in existing else []
    return {
        "available": True,
        "db_path": str(path),
        "project_id": project_id,
        "operations": ops,
        "versions": versions,
        "line_attr": line_attr,
    }


def observe_files_marks_counts(db_path: str | pathlib.Path, project_id: str) -> dict[str, Any]:
    """Independently count addressable trunk effects and unpresented agent effects via `?mode=ro`."""
    path = pathlib.Path(db_path).resolve()
    if not path.is_file():
        return {"available": False, "db_path": str(path)}
    with contextlib.closing(_connect_ro(path)) as conn:
        trunk_files = [
            r[0]
            for r in conn.execute(
                """
                SELECT DISTINCT e.file_id
                FROM source_effects e
                JOIN source_operations o ON o.id = e.operation_id
                LEFT JOIN source_presentation_watermarks w
                  ON w.project_id = e.project_id AND w.file_id = e.file_id
                WHERE e.project_id = ?
                  AND e.walk_visible = 1
                  AND o.branch_id = ''
                  AND e.ordinal > COALESCE(w.through_ordinal, 0)
                ORDER BY e.file_id ASC
                """,
                (project_id,),
            ).fetchall()
        ]
        agent_files, agent_effects = conn.execute(
            """
            SELECT COUNT(DISTINCT file_id), COUNT(*)
            FROM source_agent_presentations
            WHERE project_id = ?
            """,
            (project_id,),
        ).fetchone()
    return {
        "available": True,
        "db_path": str(path),
        "project_id": project_id,
        "marked_file_ids": trunk_files,
        "marked_file_count": len(trunk_files),
        "unpresented_agent_file_count": int(agent_files or 0),
        "unpresented_agent_effect_count": int(agent_effects or 0),
    }


def snapshot_rewind_boundary(
    project_root: str | pathlib.Path,
    db_path: str | pathlib.Path,
    tables: tuple[str, ...] = REWIND_TRACKED_TABLES,
) -> dict[str, Any]:
    """Capture independent disk SHA-256 tree + SQLite row counts for rewind boundary verification."""
    tree = hash_tree(project_root)
    counts: dict[str, int] = {}
    path = pathlib.Path(db_path).resolve()
    if not path.is_file():
        return {"available": False, "tree_hashes": tree, "table_counts": counts}
    with contextlib.closing(_connect_ro(path)) as conn:
        existing = {
            r[0]
            for r in conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            ).fetchall()
        }
        for tbl in tables:
            if tbl in existing:
                quoted = '"' + tbl.replace('"', '""') + '"'
                counts[tbl] = int(conn.execute(f"SELECT COUNT(*) FROM {quoted}").fetchone()[0])
    return {
        "available": True,
        "tree_hashes": tree,
        "table_counts": counts,
    }


def _verdict(failures: list[dict[str, Any]], missing: list[str]) -> dict[str, Any]:
    if missing:
        return {
            "verdict": "unknown",
            "passed": False,
            "missing_instrumentation": sorted(set(missing)),
            "failures": [],
            "failed_rule_ids": [],
        }
    return {
        "verdict": "pass" if not failures else "fail",
        "passed": len(failures) == 0,
        "missing_instrumentation": [],
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
    }


def evaluate_e04_attribution(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate E04 (author attribution & branch identity across trunk, worktree, worker branch)."""
    missing: list[str] = []
    failures: list[dict[str, Any]] = []

    records = evidence.get("records")
    if not isinstance(records, list) or not records:
        missing.append("missing_instrumentation:e04.records")
        return _verdict(failures, missing)

    seen_kinds: set[str] = set()
    for idx, rec in enumerate(records):
        if not isinstance(rec, dict):
            missing.append(f"missing_instrumentation:e04.records[{idx}]")
            continue
        kind = str(rec.get("author_kind") or "")
        seen_kinds.add(kind)
        if not rec.get("expected_origin") or not rec.get("recorded_origin"):
            missing.append(f"missing_instrumentation:e04.origin[{idx}]")
            continue
        if "expected_branch" not in rec or "recorded_branch" not in rec:
            missing.append(f"missing_instrumentation:e04.branch[{idx}]")
            continue
        if rec["recorded_origin"] != rec["expected_origin"]:
            failures.append({
                "rule_id": "e04_wrong_origin",
                "message": f"Record {idx} ({kind}) origin={rec['recorded_origin']} want {rec['expected_origin']}",
            })
        if rec["recorded_branch"] != rec["expected_branch"]:
            failures.append({
                "rule_id": "e04_wrong_branch",
                "message": f"Record {idx} ({kind}) branch={rec['recorded_branch']} want {rec['expected_branch']}",
            })
        if kind in {"worker", "landed_worker"}:
            exp_job = rec.get("expected_job_id")
            got_job = rec.get("recorded_job_id")
            if not exp_job:
                missing.append(f"missing_instrumentation:e04.job_id[{idx}]")
            elif got_job != exp_job:
                failures.append({
                    "rule_id": "e04_lost_worker_job_id",
                    "message": f"Record {idx} ({kind}) job_id={got_job!r} want {exp_job!r}",
                })

    if evidence.get("require_all_author_kinds", True):
        absent = sorted(set(REQUIRED_E04_AUTHOR_KINDS) - seen_kinds)
        if absent:
            failures.append({
                "rule_id": "e04_missing_author_kind",
                "message": f"Missing required E04 author kinds: {absent}",
                "details": {"missing_kinds": absent},
            })

    if evidence.get("worker_branch_moved_trunk_head") is True:
        failures.append({
            "rule_id": "e04_worker_moved_trunk_head",
            "message": "Worker branch write unexpectedly moved trunk head.",
        })
    if evidence.get("root_change_preserved_file_id") is False:
        failures.append({
            "rule_id": "e04_root_change_forked_file_id",
            "message": "Root-set change forked file identity.",
        })

    return _verdict(failures, missing)


def evaluate_e05_restore(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate E05 (restore_version SHA-256 byte equality and refused-restore immutability)."""
    missing: list[str] = []
    failures: list[dict[str, Any]] = []

    restores = evidence.get("restores")
    refusals = evidence.get("refusals")
    if not isinstance(restores, list) or not restores:
        missing.append("missing_instrumentation:e05.restores")
    if not isinstance(refusals, list) or not refusals:
        missing.append("missing_instrumentation:e05.refusals")
    if missing:
        return _verdict(failures, missing)

    seen_kinds: set[str] = set()
    for idx, case in enumerate(restores):
        kind = str(case.get("kind") or "")
        seen_kinds.add(kind)
        exp_sha = case.get("retained_sha256")
        got_sha = case.get("disk_sha256")
        if not exp_sha or not got_sha:
            missing.append(f"missing_instrumentation:e05.restores[{idx}].sha256")
            continue
        if got_sha != exp_sha:
            failures.append({
                "rule_id": "e05_restore_hash_mismatch",
                "message": f"Restore '{kind}' disk SHA-256 {got_sha} != retained {exp_sha}",
            })

    if evidence.get("require_all_restore_kinds", True):
        absent = sorted(set(REQUIRED_E05_RESTORE_KINDS) - seen_kinds)
        if absent:
            failures.append({
                "rule_id": "e05_missing_restore_kind",
                "message": f"Missing required E05 restore kinds: {absent}",
            })

    for idx, ref in enumerate(refusals):
        pre = ref.get("pre_tree_hashes")
        post = ref.get("post_tree_hashes")
        if pre is None or post is None:
            missing.append(f"missing_instrumentation:e05.refusals[{idx}].tree_hashes")
            continue
        if ref.get("refused") is not True:
            failures.append({
                "rule_id": "e05_refusal_not_enforced",
                "message": f"Refusal case '{ref.get('kind')}' was not refused.",
            })
        if pre != post:
            failures.append({
                "rule_id": "e05_refused_restore_mutated_disk",
                "message": f"Refused restore '{ref.get('kind')}' mutated files on disk.",
            })

    return _verdict(failures, missing)


def evaluate_e06_rewind(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate E06 (rewind blockers + clean/failed/restarted/retry boundaries)."""
    missing: list[str] = []
    failures: list[dict[str, Any]] = []

    blockers = evidence.get("blockers")
    executions = evidence.get("executions")
    if not isinstance(blockers, list) or not blockers:
        missing.append("missing_instrumentation:e06.blockers")
    if not isinstance(executions, list) or not executions:
        missing.append("missing_instrumentation:e06.executions")
    if missing:
        return _verdict(failures, missing)

    for idx, blk in enumerate(blockers):
        code = str(blk.get("code") or f"blocker[{idx}]")
        pre_tree = blk.get("pre_tree_hashes")
        post_tree = blk.get("post_tree_hashes")
        pre_counts = blk.get("pre_table_counts")
        post_counts = blk.get("post_table_counts")
        if pre_tree is None or post_tree is None or pre_counts is None or post_counts is None:
            missing.append(f"missing_instrumentation:e06.blockers[{code}]")
            continue
        if blk.get("blocked") is not True:
            failures.append({
                "rule_id": "e06_blocker_not_triggered",
                "message": f"Rewind blocker '{code}' was not enforced.",
            })
        if pre_tree != post_tree:
            failures.append({
                "rule_id": "e06_blocker_mutated_tree",
                "message": f"Rewind blocker '{code}' mutated the working tree.",
            })
        if pre_counts != post_counts:
            failures.append({
                "rule_id": "e06_blocker_mutated_db",
                "message": f"Rewind blocker '{code}' mutated SQLite transcript/ledger row counts.",
            })

    for idx, ex in enumerate(executions):
        kind = str(ex.get("kind") or f"exec[{idx}]")
        if ex.get("expected_tree_hashes") is None or ex.get("actual_tree_hashes") is None:
            missing.append(f"missing_instrumentation:e06.executions[{kind}].tree_hashes")
            continue
        if ex["expected_tree_hashes"] != ex["actual_tree_hashes"]:
            failures.append({
                "rule_id": "e06_partial_suffix",
                "message": f"Rewind execution '{kind}' left mismatched/partial working tree bytes.",
            })
        if ex.get("duplicate_versions_detected") is True:
            failures.append({
                "rule_id": "e06_duplicate_version",
                "message": f"Rewind execution '{kind}' created duplicate source_versions.",
            })

    return _verdict(failures, missing)


def evaluate_e07_files_marks(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate E07 (Files marks & unpresented AI counts vs independent SQLite observation)."""
    missing: list[str] = []
    failures: list[dict[str, Any]] = []

    prod = evidence.get("product_marks")
    ind = evidence.get("independent_counts")
    if not isinstance(prod, dict) or not prod:
        missing.append("missing_instrumentation:e07.product_marks")
    if not isinstance(ind, dict) or not ind:
        missing.append("missing_instrumentation:e07.independent_counts")
    if missing:
        return _verdict(failures, missing)

    prod_ids = sorted(prod.get("marked_file_ids") or [])
    ind_ids = sorted(ind.get("marked_file_ids") or [])
    if prod_ids != ind_ids:
        failures.append({
            "rule_id": "e07_marked_files_mismatch",
            "message": f"Product marked_file_ids {prod_ids} != independent {ind_ids}",
        })

    if prod.get("unpresented_agent_file_count") != ind.get("unpresented_agent_file_count"):
        failures.append({
            "rule_id": "e07_unpresented_ai_count_mismatch",
            "message": (
                f"Product unpresented AI file count {prod.get('unpresented_agent_file_count')} "
                f"!= independent {ind.get('unpresented_agent_file_count')}"
            ),
        })

    if prod.get("worker_branch_leaked") is True:
        failures.append({
            "rule_id": "e07_worker_branch_leaked",
            "message": "Worker branch file leaked into trunk Files marks.",
        })

    comparisons = prod.get("opened_comparisons") or []
    for comp in comparisons:
        if comp.get("in_range") is not True:
            failures.append({
                "rule_id": "e07_comparison_not_in_range",
                "message": f"Marked file '{comp.get('path')}' did not open an in-range comparison.",
            })

    return _verdict(failures, missing)


def evaluate_e09_worktrees_and_outside(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate E09 (multi-checkout worktree identity & out-of-root refusal)."""
    missing: list[str] = []
    failures: list[dict[str, Any]] = []

    checkouts = evidence.get("checkouts")
    outside = evidence.get("outside_refusals")
    if not isinstance(checkouts, list) or not checkouts:
        missing.append("missing_instrumentation:e09.checkouts")
    if not isinstance(outside, list) or not outside:
        missing.append("missing_instrumentation:e09.outside_refusals")
    if missing:
        return _verdict(failures, missing)

    for idx, co in enumerate(checkouts):
        if not co.get("expected_sha256") or not co.get("disk_sha256"):
            missing.append(f"missing_instrumentation:e09.checkouts[{idx}].sha256")
            continue
        if co["disk_sha256"] != co["expected_sha256"]:
            failures.append({
                "rule_id": "e09_checkout_hash_mismatch",
                "message": f"Checkout '{co.get('label')}' disk SHA-256 mismatch.",
            })
        if co.get("recorded_branch") != co.get("expected_branch"):
            failures.append({
                "rule_id": "e09_checkout_branch_mismatch",
                "message": (
                    f"Checkout '{co.get('label')}' branch={co.get('recorded_branch')!r} "
                    f"want {co.get('expected_branch')!r}"
                ),
            })

    for idx, out in enumerate(outside):
        if not out.get("pre_sha256") or not out.get("post_sha256"):
            missing.append(f"missing_instrumentation:e09.outside_refusals[{idx}].sha256")
            continue
        if out.get("refused") is not True:
            failures.append({
                "rule_id": "e09_outside_not_refused",
                "message": f"Out-of-root target '{out.get('path')}' was not refused.",
            })
        if out["pre_sha256"] != out["post_sha256"]:
            failures.append({
                "rule_id": "e09_outside_mutated",
                "message": f"Out-of-root target '{out.get('path')}' mutated on disk.",
            })

    return _verdict(failures, missing)


def parse_go_tb06_receipts(test_output: str) -> dict[str, dict[str, Any]]:
    """Parse `WEA_TB06_RECEIPT:{...}` JSON lines from `go test -v` output keyed by family."""
    receipts: dict[str, dict[str, Any]] = {}
    for line in test_output.splitlines():
        idx = line.find(RECEIPT_PREFIX)
        if idx == -1:
            continue
        raw = line[idx + len(RECEIPT_PREFIX):].strip()
        try:
            parsed = json.loads(raw)
        except ValueError:
            continue
        if isinstance(parsed, dict) and parsed.get("family"):
            receipts[str(parsed["family"])] = parsed
    return receipts


def run_go_tb06_suite(lycaon_dir: str | pathlib.Path) -> dict[str, Any]:
    """Run the real in-process Go TB06 suite (`go test -v -race -count=1 -run TestWEATB06 ./test/wea`)."""
    proc = subprocess.run(
        ["go", "test", "-v", "-race", "-count=1", "-run", "TestWEATB06", "./test/wea"],
        cwd=str(pathlib.Path(lycaon_dir).resolve()),
        capture_output=True,
        text=True,
        check=False,
    )
    combined = proc.stdout + ("\n" + proc.stderr if proc.stderr else "")
    receipts = parse_go_tb06_receipts(combined)
    return {
        "returncode": proc.returncode,
        "passed": proc.returncode == 0,
        "families": sorted(receipts.keys()),
        "receipts": receipts,
        "output": combined,
    }


def run_tb06_qualification_controls() -> dict[str, Any]:
    """Run healthy, injected-defect, and missing-instrumentation qualification controls for TB06."""
    sha_a = sha256_bytes(b"alpha\n")
    sha_b = sha256_bytes(b"beta\n")

    healthy_e04 = evaluate_e04_attribution({
        "records": [
            {"author_kind": "person", "expected_origin": "user", "recorded_origin": "user", "expected_branch": "", "recorded_branch": ""},
            {"author_kind": "agent", "expected_origin": "agent", "recorded_origin": "agent", "expected_branch": "", "recorded_branch": ""},
            {"author_kind": "worker", "expected_origin": "agent", "recorded_origin": "agent", "expected_branch": "job-1", "recorded_branch": "job-1", "expected_job_id": "job-1", "recorded_job_id": "job-1"},
            {"author_kind": "landed_worker", "expected_origin": "agent", "recorded_origin": "agent", "expected_branch": "", "recorded_branch": "", "expected_job_id": "job-1", "recorded_job_id": "job-1"},
            {"author_kind": "command", "expected_origin": "external", "recorded_origin": "external", "expected_branch": "", "recorded_branch": ""},
            {"author_kind": "external", "expected_origin": "external", "recorded_origin": "external", "expected_branch": "worktree:wt1", "recorded_branch": "worktree:wt1"},
        ],
        "worker_branch_moved_trunk_head": False,
        "root_change_preserved_file_id": True,
    })

    defect_e04 = evaluate_e04_attribution({
        "records": [
            {"author_kind": "landed_worker", "expected_origin": "agent", "recorded_origin": "agent", "expected_branch": "", "recorded_branch": "", "expected_job_id": "job-1", "recorded_job_id": ""},
        ],
        "require_all_author_kinds": False,
    })

    healthy_e05 = evaluate_e05_restore({
        "restores": [
            {"kind": k, "retained_sha256": sha_a, "disk_sha256": sha_a}
            for k in REQUIRED_E05_RESTORE_KINDS
        ],
        "refusals": [
            {"kind": "disk_conflict", "refused": True, "pre_tree_hashes": {"a.txt": sha_a}, "post_tree_hashes": {"a.txt": sha_a}},
            {"kind": "unavailable", "refused": True, "pre_tree_hashes": {"a.txt": sha_a}, "post_tree_hashes": {"a.txt": sha_a}},
        ],
    })

    defect_e05 = evaluate_e05_restore({
        "restores": [{"kind": "binary", "retained_sha256": sha_a, "disk_sha256": sha_b}],
        "refusals": [{"kind": "disk_conflict", "refused": True, "pre_tree_hashes": {"a.txt": sha_a}, "post_tree_hashes": {"a.txt": sha_b}}],
        "require_all_restore_kinds": False,
    })

    healthy_e06 = evaluate_e06_rewind({
        "blockers": [
            {
                "code": "working_file_changed",
                "blocked": True,
                "pre_tree_hashes": {"a.txt": sha_a},
                "post_tree_hashes": {"a.txt": sha_a},
                "pre_table_counts": {"messages": 2, "source_versions": 3},
                "post_table_counts": {"messages": 2, "source_versions": 3},
            }
        ],
        "executions": [
            {
                "kind": "clean_rewind",
                "expected_tree_hashes": {"a.txt": sha_a},
                "actual_tree_hashes": {"a.txt": sha_a},
                "duplicate_versions_detected": False,
            }
        ],
    })

    defect_e06 = evaluate_e06_rewind({
        "blockers": [
            {
                "code": "later_change",
                "blocked": True,
                "pre_tree_hashes": {"a.txt": sha_a},
                "post_tree_hashes": {"a.txt": sha_b},
                "pre_table_counts": {"messages": 2},
                "post_table_counts": {"messages": 1},
            }
        ],
        "executions": [
            {
                "kind": "restarted_rewind",
                "expected_tree_hashes": {"a.txt": sha_a},
                "actual_tree_hashes": {"a.txt": sha_a},
                "duplicate_versions_detected": True,
            }
        ],
    })

    healthy_e07 = evaluate_e07_files_marks({
        "product_marks": {
            "marked_file_ids": ["f1", "f2"],
            "unpresented_agent_file_count": 2,
            "worker_branch_leaked": False,
            "opened_comparisons": [{"path": "a.go", "in_range": True}],
        },
        "independent_counts": {
            "marked_file_ids": ["f1", "f2"],
            "unpresented_agent_file_count": 2,
        },
    })

    defect_e07 = evaluate_e07_files_marks({
        "product_marks": {
            "marked_file_ids": ["f1"],
            "unpresented_agent_file_count": 1,
            "worker_branch_leaked": True,
            "opened_comparisons": [{"path": "a.go", "in_range": False}],
        },
        "independent_counts": {
            "marked_file_ids": ["f1", "f2"],
            "unpresented_agent_file_count": 2,
        },
    })

    healthy_e09 = evaluate_e09_worktrees_and_outside({
        "checkouts": [
            {"label": "trunk", "expected_sha256": sha_a, "disk_sha256": sha_a, "expected_branch": "", "recorded_branch": ""},
            {"label": "wt2", "expected_sha256": sha_b, "disk_sha256": sha_b, "expected_branch": "worktree:wt2", "recorded_branch": "worktree:wt2"},
        ],
        "outside_refusals": [
            {"path": "/outside/x.txt", "refused": True, "pre_sha256": sha_a, "post_sha256": sha_a},
        ],
    })

    missing_ctrl = evaluate_e04_attribution({})

    qualified = (
        healthy_e04["verdict"] == "pass"
        and defect_e04["verdict"] == "fail" and "e04_lost_worker_job_id" in defect_e04["failed_rule_ids"]
        and healthy_e05["verdict"] == "pass"
        and defect_e05["verdict"] == "fail"
        and {"e05_restore_hash_mismatch", "e05_refused_restore_mutated_disk"} <= set(defect_e05["failed_rule_ids"])
        and healthy_e06["verdict"] == "pass"
        and defect_e06["verdict"] == "fail"
        and {"e06_blocker_mutated_tree", "e06_blocker_mutated_db", "e06_duplicate_version"} <= set(defect_e06["failed_rule_ids"])
        and healthy_e07["verdict"] == "pass"
        and defect_e07["verdict"] == "fail"
        and {"e07_marked_files_mismatch", "e07_unpresented_ai_count_mismatch", "e07_worker_branch_leaked", "e07_comparison_not_in_range"} <= set(defect_e07["failed_rule_ids"])
        and healthy_e09["verdict"] == "pass"
        and missing_ctrl["verdict"] == "unknown"
    )
    return {
        "schema": "wea-tb06-controls-v1",
        "qualified": qualified,
        "healthy": {"E04": healthy_e04, "E05": healthy_e05, "E06": healthy_e06, "E07": healthy_e07, "E09": healthy_e09},
        "defects": {"E04": defect_e04, "E05": defect_e05, "E06": defect_e06, "E07": defect_e07},
        "missing": missing_ctrl,
    }
