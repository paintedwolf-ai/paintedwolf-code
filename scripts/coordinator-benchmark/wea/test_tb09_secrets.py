"""Unit tests for TB09 · Secret screening and custody (A10, A11, A13)."""

from __future__ import annotations

import json
import pathlib
import sqlite3
import tempfile
import unittest
import urllib.request

from wea.canary import CanaryGenerator
from wea.fixtures_net import RecordingHTTPFixture
from wea.tb09_secrets import (
    audit_stores_for_canaries,
    canary_digest,
    verify_outbound_screening_option,
    verify_reference_grammar,
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


if __name__ == "__main__":
    unittest.main()
