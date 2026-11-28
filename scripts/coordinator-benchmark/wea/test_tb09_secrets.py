"""Unit tests for TB09 · Secret screening and custody (A10, A11, A13)."""

from __future__ import annotations

import json
import pathlib
import sqlite3
import tempfile
import unittest
import urllib.request
from unittest import mock

from wea.canary import CanaryGenerator
from wea.fixtures_net import RecordingHTTPFixture
from wea.provenance import REPO_ROOT, file_sha256
from wea.tb09_secrets import (
    audit_stores_for_canaries,
    canary_digest,
    verify_outbound_screening_option,
    verify_reference_grammar,
)
from wea.tb09_sidecar_runner import (
    RecordingOpenAIFixture,
    TB09_SIDECAR_SLOTS,
    TB09SlotSpec,
    _restart_application,
    build_parser,
    build_slot_receipt,
    extract_secret_card,
    mint_detectable_canary,
)


class TestTB09Secrets(unittest.TestCase):
    def test_a10_outbound_options_with_recording_fixture(self) -> None:
        gen = CanaryGenerator("tb09-unit-a10", seed="tb09-seed")
        canary = gen.mint("outbound")
        ref_token = "{{paintedwolf-secret:11111111-2222-3333-4444-555555555555}}"

        # 1. Withhold: 0 requests to recording endpoint -> passes.
        with RecordingHTTPFixture(
            fixture_id="tb09-withhold",
            routes={"/ingest": (200, {"ok": True})},
            redact_tokens=tuple(gen.tokens),
        ) as ep:
            res = verify_outbound_screening_option("withhold", ep.snapshot(), gen.tokens)
            self.assertTrue(res["passed"], res)
            self.assertEqual(res["connection_count"], 0)

        # 2. SendRedacted: endpoint receives [REDACTED], never raw canary -> passes.
        with RecordingHTTPFixture(
            fixture_id="tb09-redacted",
            routes={"/ingest": (200, {"ok": True})},
            redact_tokens=tuple(gen.tokens),
        ) as ep:
            req = urllib.request.Request(
                f"{ep.base_url}/ingest",
                data=b"token=[REDACTED]",
                method="POST",
            )
            with urllib.request.urlopen(req, timeout=3) as resp:
                self.assertEqual(resp.status, 200)
            res = verify_outbound_screening_option("send_redacted", ep.snapshot(), gen.tokens)
            self.assertTrue(res["passed"], res)
            self.assertEqual(res["canary_wire_hits"], 0)

        # 3. Protect: endpoint receives reference token, never raw canary -> passes.
        with RecordingHTTPFixture(
            fixture_id="tb09-protect",
            routes={"/ingest": (200, {"ok": True})},
            redact_tokens=tuple(gen.tokens),
        ) as ep:
            req = urllib.request.Request(
                f"{ep.base_url}/ingest",
                data=f"token={ref_token}".encode("utf-8"),
                method="POST",
            )
            with urllib.request.urlopen(req, timeout=3) as resp:
                self.assertEqual(resp.status, 200)
            res = verify_outbound_screening_option("protect", ep.snapshot(), gen.tokens)
            self.assertTrue(res["passed"], res)
            self.assertEqual(res["canary_wire_hits"], 0)

        # 4. SendUnchanged: endpoint receives verbatim canary (fixture records redacted=True, never raw bytes).
        with RecordingHTTPFixture(
            fixture_id="tb09-unchanged",
            routes={"/ingest": (200, {"ok": True})},
            redact_tokens=tuple(gen.tokens),
        ) as ep:
            req = urllib.request.Request(
                f"{ep.base_url}/ingest",
                data=f"token={canary}".encode("utf-8"),
                method="POST",
            )
            with urllib.request.urlopen(req, timeout=3) as resp:
                self.assertEqual(resp.status, 200)
            snap = ep.snapshot()
            self.assertNotIn(canary, json.dumps(snap))
            res = verify_outbound_screening_option("send_unchanged", snap, gen.tokens)
            self.assertTrue(res["passed"], res)
            self.assertEqual(res["canary_wire_hits"], 1)

            # Negative check: same snapshot MUST fail under withhold and send_redacted!
            self.assertFalse(verify_outbound_screening_option("withhold", snap, gen.tokens)["passed"])
            self.assertFalse(verify_outbound_screening_option("send_redacted", snap, gen.tokens)["passed"])

    def test_a11_reference_grammar_validation(self) -> None:
        good = "{{paintedwolf-secret:11111111-2222-3333-4444-555555555555}}"
        res_good = verify_reference_grammar(good)
        self.assertTrue(res_good["valid"])
        self.assertFalse(res_good["malformed"])
        self.assertEqual(res_good["secret_id"], "11111111-2222-3333-4444-555555555555")

        for bad in (
            "{{paintedwolf-secret:not-a-uuid}}",
            "{{paintedwolf-secret:11111111-2222-3333-4444-555555555555",
            "{{paintedwolf-secret:AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE}}",
        ):
            res_bad = verify_reference_grammar(bad)
            self.assertFalse(res_bad["valid"], bad)
            self.assertTrue(res_bad["malformed"], bad)
            self.assertIsNone(res_bad["secret_id"])

        plain = verify_reference_grammar("just ordinary text")
        self.assertFalse(plain["valid"])
        self.assertFalse(plain["malformed"])

    def test_a10_a11_audit_stores_detects_leaks_and_redacts_report_d7(self) -> None:
        gen = CanaryGenerator("tb09-unit-audit", seed="tb09-audit-seed")
        canary = gen.mint("store_leak")

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path = root / "store.db"
            log_path = root / "sidecar.log"
            export_path = root / "export.json"

            # Clean stores first.
            conn = sqlite3.connect(str(db_path))
            conn.execute("CREATE TABLE messages (id INTEGER PRIMARY KEY, content TEXT)")
            conn.execute(
                "INSERT INTO messages (content) VALUES (?)",
                ("safe ref {{paintedwolf-secret:11111111-2222-3333-4444-555555555555}}",),
            )
            conn.commit()
            conn.close()
            log_path.write_text("INFO screening active\n", encoding="utf-8")
            export_path.write_text(json.dumps({"status": "ok"}), encoding="utf-8")

            clean_report = audit_stores_for_canaries(
                gen.tokens,
                sqlite_dbs=[db_path],
                log_paths=[log_path],
                export_paths=[export_path],
            )
            self.assertTrue(clean_report["clean"])
            self.assertEqual(clean_report["leak_count"], 0)
            self.assertEqual(clean_report["canary_digests"], [canary_digest(canary)])
            self.assertNotIn(canary, json.dumps(clean_report))

            # Inject a canary leak into SQLite + log + export and verify D7 redaction.
            conn = sqlite3.connect(str(db_path))
            conn.execute("INSERT INTO messages (content) VALUES (?)", (f"leaked={canary}",))
            conn.commit()
            conn.close()
            log_path.write_text(f"WARN leaked={canary}\n", encoding="utf-8")
            export_path.write_text(json.dumps({"raw": canary}), encoding="utf-8")

            leak_report = audit_stores_for_canaries(
                gen.tokens,
                sqlite_dbs=[db_path],
                log_paths=[log_path],
                export_paths=[export_path],
            )
            self.assertFalse(leak_report["clean"])
            self.assertEqual(leak_report["leak_count"], 3)
            # D7 strict invariant: raw canary MUST NEVER appear anywhere in the leak report!
            serialized = json.dumps(leak_report)
            self.assertNotIn(canary, serialized)
            self.assertIn(canary_digest(canary), serialized)

    def test_inproc_manifest_matches_pinned_base_and_overlay_sha256(self) -> None:
        manifest_path = pathlib.Path(__file__).resolve().parent / "tb09_inproc_manifest.json"
        self.assertTrue(manifest_path.is_file(), f"missing {manifest_path}")
        data = json.loads(manifest_path.read_text(encoding="utf-8"))
        self.assertEqual(data["card"], "TB09")
        self.assertEqual(data["pinned_product_base"], "bbeb46e72ce3d1cfc052259cadcce29185bb40bd")
        self.assertEqual(len(data["slots"]), 1)
        slot = data["slots"][0]
        self.assertEqual(slot["slot_id"], "A10-inproc-01")
        self.assertEqual(slot["family"], "A10")
        self.assertEqual(slot["lane"], "in-process")
        self.assertEqual(slot["go_package"], "./internal/llm")
        self.assertEqual(slot["go_test_selector"], "TestWEATB09_A10_OutboundCardHeldPastModelDeadlineManualClock")
        overlay_file = REPO_ROOT / slot["fixture_overlay_path"]
        self.assertTrue(overlay_file.is_file(), f"missing overlay {overlay_file}")
        self.assertEqual(slot["fixture_overlay_sha256"], file_sha256(overlay_file))

    def test_sidecar_slot_catalog_matches_ledger_counts_39(self) -> None:
        self.assertEqual(len(TB09_SIDECAR_SLOTS), 39)
        ids = [s.slot_id for s in TB09_SIDECAR_SLOTS]
        self.assertEqual(len(ids), len(set(ids)), "duplicate slot_ids in TB09_SIDECAR_SLOTS")
        counts = {"A10": 0, "A11": 0, "A13": 0}
        for s in TB09_SIDECAR_SLOTS:
            self.assertIn(s.family, counts)
            counts[s.family] += 1
        self.assertEqual(counts, {"A10": 19, "A11": 12, "A13": 8})

    def test_sidecar_receipt_schema_and_d7_leak_guard(self) -> None:
        gen = CanaryGenerator("tb09-receipt-unit", seed="tb09-receipt-seed")
        canary = gen.mint("guard")
        spec = TB09SlotSpec("A10-sc-04", "A10", "model_request withhold", "model_request", "withhold")
        prov = {
            "product_source": {"executable_sha256": "a" * 64, "verified": True},
            "harness_source": {"git_head": "b" * 40, "git_tree": "c" * 40},
        }
        clean_rec = build_slot_receipt(
            spec,
            provenance=prov,
            typed_result={"code": "OUTBOUND_SECRET_DENIED"},
            independent_observation={"connections": 0},
            canary_tokens=gen.tokens,
            passed=True,
        )
        self.assertEqual(clean_rec["verdict"], "pass")
        self.assertEqual(clean_rec["lane"], "sidecar")
        self.assertTrue(clean_rec["canary_scan"]["clean"])
        self.assertNotIn(canary, json.dumps(clean_rec))

        # If a buggy caller accidentally placed raw canary into typed_result, receipt MUST fail closed!
        leaked_rec = build_slot_receipt(
            spec,
            provenance=prov,
            typed_result={"raw_oops": canary},
            independent_observation={"connections": 0},
            canary_tokens=gen.tokens,
            passed=True,
        )
        self.assertEqual(leaked_rec["verdict"], "fail")
        self.assertFalse(leaked_rec["canary_scan"]["clean"])
        self.assertEqual(leaked_rec["canary_scan"]["leak_count"], 1)

    def test_sidecar_runner_cli_parser(self) -> None:
        parser = build_parser()
        args = parser.parse_args([
            "--out", "/tmp/tb09-out",
            "--engine", "/tmp/lycaon-dev",
            "--engine-manifest", "/tmp/manifest.json",
            "--families", "A10", "A13",
            "--slot", "A10-sc-01",
        ])
        self.assertEqual(args.out, pathlib.Path("/tmp/tb09-out"))
        self.assertEqual(args.engine, pathlib.Path("/tmp/lycaon-dev"))
        self.assertEqual(args.engine_manifest, pathlib.Path("/tmp/manifest.json"))
        self.assertEqual(args.families, ["A10", "A13"])
        self.assertEqual(args.slot, ["A10-sc-01"])

    def test_restart_application_reuses_existing_profile_dir_without_file_exists_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            app_dir = pathlib.Path(tmp) / "app"
            cfg_dir = app_dir / "config"
            cfg_dir.mkdir(parents=True, exist_ok=False)
            (app_dir / "store.db").write_bytes(b"sqlite-placeholder")
            (app_dir / "sidecar.log").write_text("pre-restart log\n", encoding="utf-8")

            fake_proc = mock.MagicMock()
            fake_proc.pid = 424242
            with mock.patch("wea.tb09_sidecar_runner.subprocess.Popen", return_value=fake_proc) as popen_mock:
                restarted = _restart_application(app_dir, engine_bin=pathlib.Path("/tmp/fake-lycaon-dev"))
                try:
                    self.assertEqual(restarted.directory, app_dir.resolve())
                    self.assertEqual(restarted.config, cfg_dir.resolve())
                    self.assertEqual(restarted.process.pid, 424242)
                    self.assertTrue(restarted.url.startswith("http://127.0.0.1:"))
                    self.assertTrue((app_dir / "ownership.json").is_file())
                    popen_args = popen_mock.call_args[0][0]
                    self.assertIn("serve", popen_args)
                    self.assertIn(str(app_dir.resolve() / "store.db"), popen_args)
                finally:
                    restarted.log.close()
            # Existing pre-restart log must be preserved (opened in append mode).
            self.assertIn("pre-restart log", (app_dir / "sidecar.log").read_text(encoding="utf-8"))

    def test_recording_openai_fixture_screens_canaries_and_supports_sse(self) -> None:
        gen = CanaryGenerator("tb09-openai-unit", seed="tb09-openai-seed")
        canary = mint_detectable_canary(gen, "model_slot")
        self.assertTrue(canary.startswith("ghp_"))
        self.assertEqual(len(canary), 40)
        self.assertIn(canary, gen.tokens)

        with RecordingOpenAIFixture(fixture_id="tb09-openai-test", redact_tokens=gen.tokens) as ep:
            req = urllib.request.Request(
                f"{ep.base_url}/v1/chat/completions",
                data=json.dumps({
                    "model": "tb09-m1",
                    "stream": True,
                    "messages": [{"role": "user", "content": f"key={canary}"}],
                }).encode("utf-8"),
                method="POST",
                headers={"Content-Type": "application/json"},
            )
            with urllib.request.urlopen(req, timeout=3) as resp:
                self.assertEqual(resp.status, 200)
                body = resp.read().decode("utf-8")
                self.assertIn("[DONE]", body)
            recs = ep.chat_records_since(0)
            self.assertEqual(len(recs), 1)
            self.assertTrue(recs[0]["saw_canary"])
            # D7: raw canary MUST NOT appear in the recorded fixture JSON.
            self.assertNotIn(canary, json.dumps(recs))

    def test_blocked_verdict_preserved_and_d7_checked(self) -> None:
        gen = CanaryGenerator("tb09-blocked-unit", seed="tb09-blocked-seed")
        spec = TB09SlotSpec("A10-sc-09", "A10", "web_search send_unchanged", "web_search", "send_unchanged")
        prov = {"product_source": {"verified": True}, "harness_source": {"git_head": "a" * 40}}
        rec = build_slot_receipt(
            spec,
            provenance=prov,
            typed_result={"status": "blocked", "reason": "no external search key"},
            independent_observation={"live_product_driven": False},
            canary_tokens=gen.tokens,
            verdict="blocked",
        )
        self.assertEqual(rec["verdict"], "blocked")
        self.assertTrue(rec["canary_scan"]["clean"])

    def test_extract_secret_card_recognizes_wire_approval_plan_subject_secret(self) -> None:
        cp_model = {
            "id": "cp-model-1",
            "tool_approval": {
                "plan": {
                    "subject": {
                        "kind": "secret",
                        "title": "Review detected credential",
                        "targets": [{"kind": "secret", "label": "TB09 primary", "details": {"surface": "model_request", "destination": "TB09 primary", "rule_id": "gitleaks:github-pat"}}],
                    },
                    "options": [
                        {"id": "track_and_replace", "disabled": False},
                        {"id": "send_redacted", "disabled": False},
                        {"id": "send_unchanged", "disabled": False},
                    ],
                },
            },
        }
        card = extract_secret_card(cp_model)
        self.assertIsNotNone(card)
        assert card is not None
        self.assertEqual(card["subject_kind"], "secret")
        self.assertEqual(card["surface"], "model_request")
        self.assertTrue(card["can_track"])
        self.assertTrue(card["can_redact"])

        cp_loopback = {
            "id": "cp-loopback-1",
            "tool_approval": {
                "plan": {
                    "subject": {"kind": "action", "title": "Allow loopback"},
                    "options": [{"id": "approve_current_action", "disabled": False}],
                },
            },
        }
        self.assertIsNone(extract_secret_card(cp_loopback))


if __name__ == "__main__":
    unittest.main()
