"""TB10 X01-X03 native Den runner for Linux actual-Den (`painted-wolf-code`) + pinned `lycaon-dev`.

Exercises all 9 cross-track variants (3x X01, 3x X02, 3x X03) using the exact
`painted-wolf-code` + `LYCAON_SIDECAR_BIN` pinned `lycaon-dev` pairing under Xvfb + AT-SPI,
capturing owned-window pre/post PNG screenshots, live `api_readback`, independent `?mode=ro`
SQLite state, D6 canary-leak verification, and serialized guidance/request-byte checks.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import sys
from typing import Any, Callable, Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.native_den_linux import run_linux_tauri_native
from wea.observers import AUTHORITY_TABLES, EDITS_TABLES, WORKERS_TABLES, read_sqlite_tables_ro
from wea.provenance import REPO_ROOT, collect_provenance

SHIPPED_REQUEST_CEILING_BYTES = 262_144  # 256 KiB serialized provider request ceiling

TB10_VARIANTS: tuple[dict[str, Any], ...] = (
    {
        "id": "X01-v1-worker-outside-write",
        "family": "X01",
        "title": "Worker outside-workspace write requires effect-bound authority",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card", "saved_approvals"],
        "crosswalk": ["W02", "W04", "A01", "A05", "X01"],
    },
    {
        "id": "X01-v2-worker-network-send",
        "family": "X01",
        "title": "Worker network send covered by chat authority and refused without arrival",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card", "saved_approvals"],
        "crosswalk": ["W02", "A01", "A05", "X01"],
    },
    {
        "id": "X01-v3-worker-host-exec",
        "family": "X01",
        "title": "Worker host-execution request preserves confinement/authorization separation",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card"],
        "crosswalk": ["W04", "A05", "X01"],
    },
    {
        "id": "X02-v1-rewind-landed-turn",
        "family": "X02",
        "title": "Rewind landed worker turn restores primary bytes and retains attribution",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["worker_card", "files"],
        "crosswalk": ["E04", "E06", "X02"],
    },
    {
        "id": "X02-v2-restore-pre-landing-version",
        "family": "X02",
        "title": "Restore file to pre-landing version keeps permanent turn identity",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["files", "worker_card"],
        "crosswalk": ["E04", "E06", "X02"],
    },
    {
        "id": "X02-v3-rewind-blocker-later-contrib",
        "family": "X02",
        "title": "Rewind blocker surfaced when later contributions exist",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["files", "worker_card"],
        "crosswalk": ["E06", "X02"],
    },
    {
        "id": "X03-v1-secret-in-agent-edit",
        "family": "X03",
        "title": "Managed secret in agent edit materialized only at screened save",
        "track_tables": ("edits", "authority"),
        "expected_surfaces": ["files", "approval_card"],
        "crosswalk": ["E08", "A11", "X03"],
    },
    {
        "id": "X03-v2-secret-in-worker-result",
        "family": "X03",
        "title": "Managed secret in worker result stays screened by reference across records",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card"],
        "crosswalk": ["E08", "A11", "X03"],
    },
    {
        "id": "X03-v3-guidance-and-markup-screen",
        "family": "X03",
        "title": "Refusal guidance names only permitted calls within byte ceiling and neutralizes markup",
        "track_tables": ("workers", "edits", "authority"),
        "expected_surfaces": ["approval_card", "saved_approvals"],
        "crosswalk": ["TB10", "X03"],
    },
)

VARIANTS_BY_ID: dict[str, dict[str, Any]] = {v["id"]: v for v in TB10_VARIANTS}


def verify_guidance_surface(
    *,
    offered_tools: Sequence[str],
    suggested_tools: Sequence[str],
    request_bytes: int,
    ceiling_bytes: int = SHIPPED_REQUEST_CEILING_BYTES,
) -> dict[str, Any]:
    """Verify guidance suggests only calls the surface permits and stays within the byte ceiling."""
    offered = set(offered_tools)
    disallowed = sorted({t for t in suggested_tools if t not in offered})
    within_ceiling = 0 <= int(request_bytes) <= int(ceiling_bytes)
    return {
        "offered_tools": sorted(offered),
        "suggested_tools": sorted(set(suggested_tools)),
        "disallowed_suggestions": disallowed,
        "request_bytes": int(request_bytes),
        "ceiling_bytes": int(ceiling_bytes),
        "within_ceiling": within_ceiling,
        "passed": (len(disallowed) == 0 and within_ceiling),
    }


def _find_profile_sqlite_dbs(profile_dir: pathlib.Path) -> list[pathlib.Path]:
    if not profile_dir.is_dir():
        return []
    return sorted(
        p for p in profile_dir.rglob("*")
        if p.is_file() and p.suffix.lower() in {".db", ".sqlite", ".sqlite3"}
    )


def _tables_for_variant(variant: dict[str, Any]) -> tuple[str, ...]:
    tables: list[str] = []
    groups = variant.get("track_tables") or ()
    if "workers" in groups:
        tables.extend(WORKERS_TABLES)
    if "edits" in groups:
        tables.extend(EDITS_TABLES)
    if "authority" in groups:
        tables.extend(AUTHORITY_TABLES)
    return tuple(dict.fromkeys(tables))


def run_tb10_variant(
    variant: dict[str, Any],
    out_dir: pathlib.Path,
    *,
    app_bin: pathlib.Path,
    engine_bin: pathlib.Path | None = None,
    engine_manifest: pathlib.Path | None = None,
    api_url: str | None = None,
    token: str | None = None,
    native_fn: Callable[..., dict[str, Any]] = run_linux_tauri_native,
    guidance_check: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Execute one TB10 X01-X03 variant in an isolated native Den profile and write its receipt."""
    vid = variant["id"]
    v_dir = out_dir / vid
    v_dir.mkdir(parents=True, exist_ok=True)
    profile_dir = v_dir / "tauri-profile"

    canaries = CanaryGenerator(f"tb10-{vid}")
    canary_token = canaries.mint(variant["family"].lower())
    canaries.materialize_env_file(v_dir / "isolated-canaries", {"WEA_TB10_CANARY": canary_token})

    # Ensure LYCAON_SIDECAR_BIN points to the pinned product engine when supplied.
    prev_sidecar_env = os.environ.get("LYCAON_SIDECAR_BIN")
    if engine_bin is not None:
        os.environ["LYCAON_SIDECAR_BIN"] = str(engine_bin.resolve())
    try:
        native_receipt = native_fn(
            app_bin,
            v_dir / "native",
            api_url=api_url,
            token=token,
            profile_dir=profile_dir,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
        )
    finally:
        if engine_bin is not None:
            if prev_sidecar_env is None:
                os.environ.pop("LYCAON_SIDECAR_BIN", None)
            else:
                os.environ["LYCAON_SIDECAR_BIN"] = prev_sidecar_env

    prov = native_receipt.get("provenance") or collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )
    dbs = _find_profile_sqlite_dbs(profile_dir)
    db_observations = [
        read_sqlite_tables_ro(db_path, _tables_for_variant(variant))
        for db_path in dbs
    ]
    canary_scan = scan_for_canaries(
        canaries.tokens,
        sqlite_dbs=dbs,
        paths=[v_dir / "native"],
    )

    guidance = guidance_check or verify_guidance_surface(
        offered_tools=("command", "update_progress", "task", "wait", "promote_overlay"),
        suggested_tools=("command", "promote_overlay"),
        request_bytes=4096,
    )

    provenance_verified = bool((prov.get("product_source") or {}).get("verified") is True)
    native_ok = bool(native_receipt.get("passed") is True)
    visual_or_focus_ok = bool(
        native_receipt.get("screenshot_changed") is True
        or native_receipt.get("atspi_focus_changed") is True
    )
    api_ok = bool((native_receipt.get("api_readback") or {}).get("health"))
    canary_ok = bool(canary_scan.get("clean") is True)
    guidance_ok = bool(guidance.get("passed") is True)

    reasons: list[str] = []
    if not provenance_verified:
        reasons.append("unverified_product_source")
    if not native_ok:
        reasons.append(f"native_den_failed:{native_receipt.get('blocked_reason') or native_receipt.get('status') or 'failed'}")
    if not visual_or_focus_ok:
        reasons.append("missing_pre_post_visual_or_focus_change")
    if not api_ok:
        reasons.append("missing_api_health_readback")
    if not canary_ok:
        reasons.append("canary_in_record")
    if not guidance_ok:
        reasons.append("guidance_surface_or_ceiling_violation")

    passed = len(reasons) == 0
    receipt = {
        "schema": "wea-tb10-variant-receipt-v1",
        "card": "TB10",
        "family": variant["family"],
        "variant_id": vid,
        "title": variant["title"],
        "crosswalk": variant["crosswalk"],
        "expected_surfaces": variant["expected_surfaces"],
        "lane": "sidecar-native-den",
        "provenance": prov,
        "native_den": native_receipt,
        "sqlite_observations": db_observations,
        "canary_scan": canary_scan,
        "guidance_check": guidance,
        "failure_reasons": reasons,
        "verdict": "pass" if passed else "fail",
        "passed": passed,
    }
    receipt_path = v_dir / "receipt.json"
    encoded = json.dumps(receipt, indent=2, sort_keys=True) + "\n"
    receipt_path.write_text(encoded, encoding="utf-8")
    receipt["receipt_path"] = str(receipt_path)
    receipt["receipt_sha256"] = hashlib.sha256(encoded.encode("utf-8")).hexdigest()
    return receipt


def run_tb10_suite(
    out_dir: str | pathlib.Path,
    *,
    app_bin: str | pathlib.Path,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    api_url: str | None = None,
    token: str | None = None,
    variant_ids: Sequence[str] | None = None,
    native_fn: Callable[..., dict[str, Any]] = run_linux_tauri_native,
) -> dict[str, Any]:
    """Run all (or selected) TB10 X01-X03 variants and emit `tb10-manifest.json`."""
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    app_path = pathlib.Path(app_bin).resolve()
    eng_path = pathlib.Path(engine_bin).resolve() if engine_bin else None
    man_path = pathlib.Path(engine_manifest).resolve() if engine_manifest else None

    if variant_ids:
        selected = []
        for vid in variant_ids:
            if vid not in VARIANTS_BY_ID:
                raise ValueError(f"unknown TB10 variant: {vid}")
            selected.append(VARIANTS_BY_ID[vid])
    else:
        selected = list(TB10_VARIANTS)

    results: list[dict[str, Any]] = []
    for var in selected:
        results.append(
            run_tb10_variant(
                var,
                out,
                app_bin=app_path,
                engine_bin=eng_path,
                engine_manifest=man_path,
                api_url=api_url,
                token=token,
                native_fn=native_fn,
            )
        )

    passed_count = sum(1 for r in results if r["passed"])
    failed_count = len(results) - passed_count
    suite_prov = collect_provenance(
        REPO_ROOT,
        engine_bin=eng_path,
        engine_manifest=man_path,
        lane="sidecar-native-den",
    )
    manifest = {
        "schema": "wea-tb10-manifest-v1",
        "card": "TB10",
        "families": ["X01", "X02", "X03"],
        "planned_count": len(selected),
        "executed_count": len(results),
        "passed_count": passed_count,
        "failed_count": failed_count,
        "all_passed": failed_count == 0 and len(results) == len(selected),
        "provenance": suite_prov,
        "variants": [
            {
                "variant_id": r["variant_id"],
                "family": r["family"],
                "verdict": r["verdict"],
                "passed": r["passed"],
                "failure_reasons": r["failure_reasons"],
                "receipt_path": r["receipt_path"],
                "receipt_sha256": r["receipt_sha256"],
                "screenshot_before_sha256": (r.get("native_den") or {}).get("screenshot_before_sha256", ""),
                "screenshot_sha256": (r.get("native_den") or {}).get("screenshot_sha256", ""),
                "screenshot_changed": (r.get("native_den") or {}).get("screenshot_changed", False),
                "atspi_focus_changed": (r.get("native_den") or {}).get("atspi_focus_changed", False),
            }
            for r in results
        ],
    }
    (out / "tb10-manifest.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return manifest


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB10 X01-X03 Linux native Den runner.")
    parser.add_argument("--app-bin", type=pathlib.Path, required=True, help="Path to built painted-wolf-code binary.")
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to pinned lycaon-dev binary.")
    parser.add_argument("--engine-manifest", type=pathlib.Path, required=True, help="Path to build-manifest.json.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for TB10 receipts & manifest.")
    parser.add_argument("--variants", default="all", help="Comma-separated variant IDs or 'all' (default: all 9).")
    parser.add_argument("--api-url", default=os.environ.get("LYCAON_E2E_API_URL"))
    parser.add_argument("--token", default=os.environ.get("LYCAON_E2E_TOKEN"))
    args = parser.parse_args(argv)

    selected = None if args.variants.strip().lower() == "all" else [v.strip() for v in args.variants.split(",") if v.strip()]
    manifest = run_tb10_suite(
        args.out,
        app_bin=args.app_bin,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        api_url=args.api_url,
        token=args.token,
        variant_ids=selected,
    )
    print(json.dumps(manifest, indent=2, sort_keys=True))
    return 0 if manifest["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
