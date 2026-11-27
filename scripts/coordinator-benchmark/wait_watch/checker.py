"""Pure deterministic verifier and 10-control qualification suite for wait/watch evidence bundles."""
from __future__ import annotations

import copy
import hashlib
import json
from typing import Any

SCHEMA_VERSION = 1

DEFECT_CODES = (
    "missing_delivery",
    "duplicate_presentation",
    "wrong_recipient",
    "wrong_attempt",
    "terminal_before_report",
)

POSITIVE_CONTROL_NAMES = (
    "healthy_coalescing",
    "late_receipt_refs",
    "expected_refusal",
    "ordinary_lag",
    "retention_gaps",
)

REQUIRED_OBSERVERS = ("source", "storage", "admission", "rendered_input")


def _digest(obj: Any) -> str:
    raw = json.dumps(obj, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def _normalize_scope(rec: dict[str, Any]) -> str:
    scope = rec.get("destination_scope") or ""
    if scope:
        return str(scope)
    sid = rec.get("session_id") or ""
    jid = rec.get("job_id") or ""
    return f"job:{jid}" if jid else f"session:{sid}"


def check_bundle(bundle: dict[str, Any]) -> dict[str, Any]:
    """Deterministically verify a versioned wait/watch evidence bundle (sidecar or in-process).

    Elapsed wall-clock time is diagnostic only and never affects the verdict.
    Missing required instrumentation always yields verdict='unknown' (never 'pass').
    """
    missing_instrumentation: list[str] = []
    if not isinstance(bundle, dict):
        return {
            "schema_version": SCHEMA_VERSION,
            "verdict": "unknown",
            "defects": [],
            "missing_instrumentation": ["bundle_not_object"],
            "diagnostics": {},
        }

    if bundle.get("schema_version") != SCHEMA_VERSION:
        missing_instrumentation.append("schema_version")
    lane = bundle.get("lane")
    if lane not in {"sidecar", "in-process"}:
        missing_instrumentation.append("lane")
    clock_domain = bundle.get("clock_domain")
    if clock_domain not in {"system", "manual"}:
        missing_instrumentation.append("clock_domain")

    observers = bundle.get("observers")
    if not isinstance(observers, dict):
        missing_instrumentation.append("observers")
        observers = {}
    else:
        for req_obs in REQUIRED_OBSERVERS:
            val = observers.get(req_obs)
            if not isinstance(val, dict) or not val:
                missing_instrumentation.append(f"observer:{req_obs}")
        if isinstance(observers.get("storage"), dict) and observers["storage"].get("available") is False:
            missing_instrumentation.append("observer:storage:unavailable")

    for gap in bundle.get("instrumentation_gaps") or ():
        missing_instrumentation.append(str(gap))

    # Extract diagnostic-only elapsed time (never used for verdict).
    diagnostics: dict[str, Any] = {
        "lane": lane,
        "clock_domain": clock_domain,
        "case_id": bundle.get("case_id", ""),
        "elapsed_ms_diagnostic_only": bundle.get("elapsed_ms"),
        "non_required_retention_gaps": list(bundle.get("retention_gaps") or ()),
    }

    if missing_instrumentation:
        return {
            "schema_version": SCHEMA_VERSION,
            "verdict": "unknown",
            "defects": [],
            "missing_instrumentation": list(dict.fromkeys(missing_instrumentation)),
            "diagnostics": diagnostics,
        }

    expectations = bundle.get("expectations")
    if not isinstance(expectations, dict):
        return {
            "schema_version": SCHEMA_VERSION,
            "verdict": "unknown",
            "defects": [],
            "missing_instrumentation": ["expectations"],
            "diagnostics": diagnostics,
        }

    rendered = observers["rendered_input"]
    storage = observers["storage"]
    source = observers["source"]
    presentations = [p for p in (rendered.get("presentations") or ()) if isinstance(p, dict)]
    new_presentations = [
        p for p in presentations
        if p.get("presentation_kind", "new_model_presentation") == "new_model_presentation"
    ]

    defects: list[dict[str, Any]] = []
    expected_outcome = expectations.get("expected_outcome", "delivered")
    owed_recipients = set(expectations.get("owed_recipients") or ())
    owed_occurrences = list(expectations.get("owed_occurrences") or ())
    expected_attempt = expectations.get("expected_attempt")
    expected_invocation_id = expectations.get("expected_invocation_id")
    expected_revision = expectations.get("expected_revision")
    expected_exit_code = expectations.get("expected_exit_code")
    expected_report_sha256 = expectations.get("expected_report_sha256")
    report_published_seq = expectations.get("report_published_seq")
    if report_published_seq is None:
        report_published_seq = (source.get("facts") or {}).get("report_published_seq")

    # 1. Expected refusal control path.
    if expected_outcome == "refused":
        expected_code = expectations.get("refusal_code")
        refusals = [p for p in new_presentations if p.get("outcome") == "refused"]
        if not refusals:
            defects.append({
                "code": "missing_delivery",
                "detail": "expected typed refusal presentation was not observed",
            })
        elif expected_code and not any(p.get("refusal_code") == expected_code for p in refusals):
            defects.append({
                "code": "wrong_attempt",
                "detail": f"refusal code mismatch: want {expected_code}",
            })
        # Also verify no wrong-recipient presentation occurred.
        if owed_recipients:
            for p in new_presentations:
                scope = _normalize_scope(p)
                if scope not in owed_recipients:
                    defects.append({
                        "code": "wrong_recipient",
                        "detail": f"presentation targeted {scope} outside owed recipients {sorted(owed_recipients)}",
                    })
    else:
        # 2. Check wrong_recipient across all new model presentations.
        if owed_recipients:
            for p in new_presentations:
                scope = _normalize_scope(p)
                if scope not in owed_recipients:
                    defects.append({
                        "code": "wrong_recipient",
                        "detail": f"presentation targeted {scope} not in owed_recipients {sorted(owed_recipients)}",
                        "presentation_seq": p.get("event_seq"),
                    })

        # 3. Check duplicate_presentation and missing_delivery over (recipient_scope, occurrence_id).
        # Coalescing multiple occurrence_ids into ONE new_model_presentation is healthy!
        seen_pairs: dict[tuple[str, str], list[dict[str, Any]]] = {}
        seen_claims: dict[str, int] = {}
        for p in new_presentations:
            scope = _normalize_scope(p)
            claim_id = p.get("claim_id") or ""
            if claim_id:
                seen_claims[claim_id] = seen_claims.get(claim_id, 0) + 1
                if seen_claims[claim_id] > 1:
                    defects.append({
                        "code": "duplicate_presentation",
                        "detail": f"claim_id {claim_id} presented {seen_claims[claim_id]} times as new_model_presentation",
                        "claim_id": claim_id,
                    })
            occ_ids = list(p.get("coalesced_occurrence_ids") or ())
            if not occ_ids and p.get("occurrence_id"):
                occ_ids = [p["occurrence_id"]]
            for oid in occ_ids:
                key = (scope, str(oid))
                seen_pairs.setdefault(key, []).append(p)

        for (scope, oid), plist in seen_pairs.items():
            if len(plist) > 1:
                defects.append({
                    "code": "duplicate_presentation",
                    "detail": f"occurrence {oid} presented {len(plist)} times to {scope}",
                    "occurrence_id": oid,
                    "destination_scope": scope,
                })

        for oid in owed_occurrences:
            delivered_for_oid = [
                p for (scope, seen_oid), plist in seen_pairs.items()
                if seen_oid == str(oid) and (not owed_recipients or scope in owed_recipients)
                for p in plist
            ]
            if not delivered_for_oid:
                defects.append({
                    "code": "missing_delivery",
                    "detail": f"owed occurrence {oid} had zero new_model_presentation deliveries",
                    "occurrence_id": oid,
                })

        # 4. Check wrong_attempt (stale attempt, invocation_id, or revision).
        for p in new_presentations:
            if expected_attempt is not None and p.get("attempt") is not None and p.get("attempt") != expected_attempt:
                defects.append({
                    "code": "wrong_attempt",
                    "detail": f"presentation attempt {p.get('attempt')} != expected {expected_attempt}",
                    "presentation_seq": p.get("event_seq"),
                })
            if (
                expected_invocation_id
                and p.get("invocation_id")
                and p.get("invocation_id") != expected_invocation_id
            ):
                defects.append({
                    "code": "wrong_attempt",
                    "detail": f"presentation invocation_id {p.get('invocation_id')} != expected {expected_invocation_id}",
                    "presentation_seq": p.get("event_seq"),
                })
            if (
                expected_revision is not None
                and p.get("revision") is not None
                and p.get("revision") != expected_revision
            ):
                defects.append({
                    "code": "wrong_attempt",
                    "detail": f"presentation revision {p.get('revision')} != expected {expected_revision}",
                    "presentation_seq": p.get("event_seq"),
                })

        # 5. Check terminal_before_report (publication-before-delivery order and readable report).
        for p in new_presentations:
            is_terminal = bool(p.get("terminal", True))
            if not is_terminal:
                continue
            delivery_seq = p.get("event_seq")
            if (
                report_published_seq is not None
                and delivery_seq is not None
                and delivery_seq < report_published_seq
            ):
                defects.append({
                    "code": "terminal_before_report",
                    "detail": (
                        f"terminal delivery event_seq={delivery_seq} preceded "
                        f"report_published_seq={report_published_seq}"
                    ),
                    "presentation_seq": delivery_seq,
                    "report_published_seq": report_published_seq,
                })
            if p.get("report_readable") is False:
                defects.append({
                    "code": "terminal_before_report",
                    "detail": "terminal delivery arrived while report was unreadable",
                    "presentation_seq": delivery_seq,
                })
            if expected_report_sha256:
                actual_report = p.get("report")
                if not actual_report:
                    defects.append({
                        "code": "terminal_before_report",
                        "detail": "terminal delivery missing expected report body",
                        "presentation_seq": delivery_seq,
                    })
                else:
                    actual_sha = hashlib.sha256(str(actual_report).encode("utf-8")).hexdigest()
                    if actual_sha != expected_report_sha256 and p.get("report_sha256") != expected_report_sha256:
                        defects.append({
                            "code": "terminal_before_report",
                            "detail": "terminal delivery report digest does not match published report",
                            "presentation_seq": delivery_seq,
                        })
            if expected_exit_code is not None and p.get("exit_code") is not None and p.get("exit_code") != expected_exit_code:
                defects.append({
                    "code": "wrong_attempt",
                    "detail": f"exit_code {p.get('exit_code')} != expected {expected_exit_code}",
                    "presentation_seq": delivery_seq,
                })

    # Check for leaked armed leases if expectations require settled leases.
    if expectations.get("require_zero_armed_leases") and (storage.get("armed_lease_count") or 0) > 0:
        defects.append({
            "code": "missing_delivery",
            "detail": f"storage still has {storage.get('armed_lease_count')} armed wait_leases after terminal delivery",
        })

    defect_codes = list(dict.fromkeys(d["code"] for d in defects))
    diagnostics["new_model_presentations"] = len(new_presentations)
    diagnostics["total_rendered_observations"] = len(presentations)
    diagnostics["defect_codes"] = defect_codes
    return {
        "schema_version": SCHEMA_VERSION,
        "verdict": "fail" if defects else "pass",
        "defects": defects,
        "defect_codes": defect_codes,
        "missing_instrumentation": [],
        "diagnostics": diagnostics,
    }


def _healthy_baseline_bundle() -> dict[str, Any]:
    report = "handle=command-1 termination=exited exit_code=17\nww02-c01-exit-17:stdout:ok\n"
    report_sha = hashlib.sha256(report.encode("utf-8")).hexdigest()
    return {
        "schema_version": SCHEMA_VERSION,
        "run_id": "control-baseline",
        "candidate_head": "43826a98a919a2f019d0f25925e945bdaf991c21",
        "host": {"os": "control", "arch": "control"},
        "lane": "sidecar",
        "clock_domain": "system",
        "case_id": "c01-parked-exit",
        "seed": 1,
        "elapsed_ms": 120,
        "expectations": {
            "expected_outcome": "delivered",
            "owed_recipients": ["session:sess-1"],
            "owed_occurrences": ["occ-1"],
            "expected_attempt": 1,
            "expected_invocation_id": "inv-1",
            "expected_revision": 1,
            "expected_exit_code": 17,
            "expected_report_sha256": report_sha,
            "report_published_seq": 2,
            "require_zero_armed_leases": True,
        },
        "observers": {
            "source": {
                "observer": "source",
                "clock_domain": "system",
                "facts": {
                    "expected_exit_code": 17,
                    "report_published_seq": 2,
                    "expected_payload_sha256": report_sha,
                },
                "events": [{"event_seq": 1, "kind": "reached"}, {"event_seq": 2, "kind": "report_published"}],
            },
            "storage": {
                "observer": "storage",
                "clock_domain": "system",
                "available": True,
                "armed_lease_count": 0,
                "tables": {"wait_leases": [{"id": "lease-1", "session_id": "sess-1", "status": "resolved"}]},
            },
            "admission": {
                "observer": "admission",
                "clock_domain": "system",
                "executions": [{"submission_id": "sub-1", "settled": True}],
                "sse_frames": [{"event_seq": 2, "event_type": "wait_lease.resolved"}],
            },
            "rendered_input": {
                "observer": "rendered_input",
                "clock_domain": "system",
                "presentations": [
                    {
                        "event_seq": 3,
                        "turn": 2,
                        "clock_domain": "system",
                        "presentation_kind": "new_model_presentation",
                        "session_id": "sess-1",
                        "destination_scope": "session:sess-1",
                        "handle": "command-1",
                        "invocation_id": "inv-1",
                        "occurrence_id": "occ-1",
                        "claim_id": "claim-1",
                        "attempt": 1,
                        "revision": 1,
                        "exit_code": 17,
                        "terminal": True,
                        "report": report,
                        "report_readable": True,
                        "report_sha256": report_sha,
                    }
                ],
            },
            "den": {"observer": "den", "status": "not_run"},
        },
    }


def build_control_bundles() -> dict[str, dict[str, Any]]:
    """Build all 11 checker qualification controls (5 negative, 5 positive, 1 missing-instrumentation)."""
    base = _healthy_baseline_bundle()
    controls: dict[str, dict[str, Any]] = {}

    # 5 Negative Controls (injecting defects into copies of healthy evidence)
    neg_missing = copy.deepcopy(base)
    neg_missing["observers"]["rendered_input"]["presentations"] = []
    controls["missing_delivery"] = {
        "kind": "negative",
        "expected_verdict": "fail",
        "expected_defect": "missing_delivery",
        "bundle": neg_missing,
    }

    neg_dup = copy.deepcopy(base)
    p0 = copy.deepcopy(neg_dup["observers"]["rendered_input"]["presentations"][0])
    p1 = copy.deepcopy(p0)
    p1["event_seq"] = 4
    p1["turn"] = 3
    neg_dup["observers"]["rendered_input"]["presentations"] = [p0, p1]
    controls["duplicate_presentation"] = {
        "kind": "negative",
        "expected_verdict": "fail",
        "expected_defect": "duplicate_presentation",
        "bundle": neg_dup,
    }

    neg_recip = copy.deepcopy(base)
    neg_recip["observers"]["rendered_input"]["presentations"][0]["session_id"] = "sess-other"
    neg_recip["observers"]["rendered_input"]["presentations"][0]["destination_scope"] = "session:sess-other"
    controls["wrong_recipient"] = {
        "kind": "negative",
        "expected_verdict": "fail",
        "expected_defect": "wrong_recipient",
        "bundle": neg_recip,
    }

    neg_attempt = copy.deepcopy(base)
    neg_attempt["observers"]["rendered_input"]["presentations"][0]["attempt"] = 2
    neg_attempt["observers"]["rendered_input"]["presentations"][0]["invocation_id"] = "inv-stale"
    controls["wrong_attempt"] = {
        "kind": "negative",
        "expected_verdict": "fail",
        "expected_defect": "wrong_attempt",
        "bundle": neg_attempt,
    }

    neg_tbr = copy.deepcopy(base)
    neg_tbr["observers"]["rendered_input"]["presentations"][0]["event_seq"] = 1  # < report_published_seq (2)
    neg_tbr["observers"]["rendered_input"]["presentations"][0]["report_readable"] = False
    controls["terminal_before_report"] = {
        "kind": "negative",
        "expected_verdict": "fail",
        "expected_defect": "terminal_before_report",
        "bundle": neg_tbr,
    }

    # 5 Positive / Benign Controls (must pass without false positives)
    pos_coalesce = copy.deepcopy(base)
    pos_coalesce["expectations"]["owed_occurrences"] = ["occ-1", "occ-2", "occ-3"]
    pos_coalesce["observers"]["rendered_input"]["presentations"][0]["coalesced_occurrence_ids"] = [
        "occ-1",
        "occ-2",
        "occ-3",
    ]
    controls["healthy_coalescing"] = {
        "kind": "positive",
        "expected_verdict": "pass",
        "expected_defect": None,
        "bundle": pos_coalesce,
    }

    pos_late_ref = copy.deepcopy(base)
    pos_late_ref["observers"]["rendered_input"]["presentations"][0]["retained_ref"] = "output:command-1#16"
    pos_late_ref["observers"]["rendered_input"]["presentations"][0]["late_receipt_ref_resolved"] = True
    controls["late_receipt_refs"] = {
        "kind": "positive",
        "expected_verdict": "pass",
        "expected_defect": None,
        "bundle": pos_late_ref,
    }

    pos_refusal = copy.deepcopy(base)
    pos_refusal["expectations"] = {
        "expected_outcome": "refused",
        "refusal_code": "WATCH_CAP_EXCEEDED",
        "owed_recipients": ["session:sess-1"],
        "require_zero_armed_leases": True,
    }
    pos_refusal["observers"]["rendered_input"]["presentations"] = [
        {
            "event_seq": 3,
            "turn": 1,
            "clock_domain": "system",
            "presentation_kind": "new_model_presentation",
            "session_id": "sess-1",
            "destination_scope": "session:sess-1",
            "outcome": "refused",
            "refusal_code": "WATCH_CAP_EXCEEDED",
            "terminal": False,
        }
    ]
    controls["expected_refusal"] = {
        "kind": "positive",
        "expected_verdict": "pass",
        "expected_defect": None,
        "bundle": pos_refusal,
    }

    pos_lag = copy.deepcopy(base)
    pos_lag["elapsed_ms"] = 45000  # High wall-clock lag must NOT fail deterministic check
    controls["ordinary_lag"] = {
        "kind": "positive",
        "expected_verdict": "pass",
        "expected_defect": None,
        "bundle": pos_lag,
    }

    pos_retention = copy.deepcopy(base)
    pos_retention["retention_gaps"] = ["old_preview_compacted"]
    # Also include a history re-read and SSE reconnect repaint to prove they are not duplicate presentations.
    reread = copy.deepcopy(pos_retention["observers"]["rendered_input"]["presentations"][0])
    reread["event_seq"] = 4
    reread["presentation_kind"] = "history_reread"
    repaint = copy.deepcopy(pos_retention["observers"]["rendered_input"]["presentations"][0])
    repaint["event_seq"] = 5
    repaint["presentation_kind"] = "sse_reconnect_repaint"
    pos_retention["observers"]["rendered_input"]["presentations"].extend([reread, repaint])
    controls["retention_gaps"] = {
        "kind": "positive",
        "expected_verdict": "pass",
        "expected_defect": None,
        "bundle": pos_retention,
    }

    # Missing-instrumentation control (must yield verdict='unknown', never 'pass')
    missing_inst = copy.deepcopy(base)
    del missing_inst["observers"]["rendered_input"]
    controls["missing_instrumentation"] = {
        "kind": "instrumentation",
        "expected_verdict": "unknown",
        "expected_defect": None,
        "bundle": missing_inst,
    }

    return controls


def run_checker_controls() -> dict[str, Any]:
    """Execute all 11 checker controls and return a structured qualification report."""
    controls = build_control_bundles()
    results: dict[str, Any] = {}
    all_passed = True
    for name, spec in controls.items():
        got = check_bundle(spec["bundle"])
        expected_verdict = spec["expected_verdict"]
        expected_defect = spec["expected_defect"]
        ok = got["verdict"] == expected_verdict
        if expected_defect is not None:
            ok = ok and (expected_defect in got.get("defect_codes", []))
        if not ok:
            all_passed = False
        results[name] = {
            "kind": spec["kind"],
            "expected_verdict": expected_verdict,
            "expected_defect": expected_defect,
            "actual_verdict": got["verdict"],
            "actual_defect_codes": got.get("defect_codes", []),
            "missing_instrumentation": got.get("missing_instrumentation", []),
            "passed": ok,
        }
    return {
        "schema_version": SCHEMA_VERSION,
        "all_passed": all_passed,
        "control_count": len(results),
        "controls": results,
        "report_sha256": _digest(results),
    }
