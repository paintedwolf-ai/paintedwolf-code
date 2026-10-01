"""Advisory severity catalog builder: NVD metric selection and catalog row identity."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "build-advisory-severity.py"
spec = importlib.util.spec_from_file_location("build_advisory_severity", SCRIPT)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)

CATALOG_KEYS = {"id", "type", "vector", "source"}
V31_PRIMARY = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
V31_SECONDARY = "CVSS:3.1/AV:L/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N"
V40 = "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N"


def metric(vector, score, kind="Primary"):
    return {"source": "nvd@nist.gov" if kind == "Primary" else "cna@example.org", "type": kind,
            "cvssData": {"vectorString": vector, "baseScore": score}}


class FakeResponse:
    status = 200

    def __init__(self, body):
        self.body = json.dumps(body).encode("utf-8")

    def read(self):
        return self.body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def nvd(records):
    """urlopen stand-in serving NVD 2.0 responses keyed by CVE id."""
    def urlopen(request, timeout=None):
        cve_id = request.full_url.rsplit("cveId=", 1)[1]
        if cve_id not in records:
            return FakeResponse({"vulnerabilities": []})
        return FakeResponse({"vulnerabilities": [{"cve": {"id": cve_id, "metrics": records[cve_id]}}]})
    return urlopen


class AdvisorySeverityBuilderTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.out = Path(temporary.name) / "advisory-severity.json"

    def build(self, records, *cves):
        """Run the builder against the fake NVD; return (exit status, rows)."""
        argv = [str(SCRIPT), "--out", str(self.out)]
        for cve in cves:
            argv += ["--cve", cve]
        status = 0
        with patch.object(sys, "argv", argv), patch("urllib.request.urlopen", nvd(records)), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            try:
                builder.main()
            except SystemExit as exit:
                status = exit.code
        return status, json.loads(self.out.read_text(encoding="utf-8"))

    def test_v4_only_record_is_typed_cvss_v4(self):
        status, rows = self.build({"CVE-2025-0004": {"cvssMetricV40": [metric(V40, 9.3)]}}, "CVE-2025-0004")
        self.assertEqual(status, 0)
        self.assertEqual(rows, [{"id": "CVE-2025-0004", "type": "CVSS_V4", "vector": V40, "source": "nvd.cvss"}],
                         "a CVSS 4.0 metric must produce a CVSS_V4 row; the catalog scores the vector by its type")

    def test_v3_is_preferred_over_v4(self):
        status, rows = self.build({"CVE-2025-0005": {"cvssMetricV40": [metric(V40, 9.3)],
                                                     "cvssMetricV31": [metric(V31_PRIMARY, 9.8)]}}, "CVE-2025-0005")
        self.assertEqual(status, 0)
        self.assertEqual([(row["type"], row["vector"]) for row in rows], [("CVSS_V3", V31_PRIMARY)])

    def test_nvd_primary_metric_wins_over_secondary(self):
        records = {"CVE-2025-0006": {"cvssMetricV31": [metric(V31_SECONDARY, 2.5, "Secondary"),
                                                       metric(V31_PRIMARY, 9.8, "Primary")]}}
        status, rows = self.build(records, "CVE-2025-0006")
        self.assertEqual(status, 0)
        self.assertEqual([row["vector"] for row in rows], [V31_PRIMARY],
                         "NVD's Primary assessment must win over a CNA's Secondary score listed first")

    def test_secondary_metric_is_used_when_no_primary_exists(self):
        status, rows = self.build({"CVE-2025-0007": {"cvssMetricV31": [metric(V31_SECONDARY, 2.5, "Secondary")]}},
                                  "CVE-2025-0007")
        self.assertEqual(status, 0)
        self.assertEqual([row["vector"] for row in rows], [V31_SECONDARY])

    def test_existing_ids_are_normalized_and_deduplicated(self):
        self.out.write_text(json.dumps([
            {"id": " cve-2025-0008 ", "type": "CVSS_V3", "vector": V31_SECONDARY, "source": "nvd.cvss"},
            {"id": "CVE-2025-0001", "type": "CVSS_V3", "vector": V31_PRIMARY, "source": "nvd.cvss", "score": 9.8},
        ]), encoding="utf-8")
        status, rows = self.build({"CVE-2025-0008": {"cvssMetricV31": [metric(V31_PRIMARY, 9.8)]}}, "cve-2025-0008")
        self.assertEqual(status, 0)
        self.assertEqual([row["id"] for row in rows], ["CVE-2025-0001", "CVE-2025-0008"],
                         "catalog ids must be normalized (trimmed, upper case) so a refresh replaces the existing row")
        self.assertEqual(rows[1]["vector"], V31_PRIMARY)

    def test_rows_carry_only_catalog_fields(self):
        self.out.write_text(json.dumps([
            {"id": "CVE-2025-0001", "type": "CVSS_V3", "vector": V31_PRIMARY, "source": "nvd.cvss", "score": 9.8},
        ]), encoding="utf-8")
        status, rows = self.build({"CVE-2025-0009": {"cvssMetricV31": [metric(V31_PRIMARY, 9.8)]}}, "CVE-2025-0009")
        self.assertEqual(status, 0)
        for row in rows:
            self.assertEqual(set(row), CATALOG_KEYS,
                             "rows carry no score: the engine derives the base score from the vector")

    def test_unresolved_cve_fails_after_writing_resolved_rows(self):
        status, rows = self.build({"CVE-2025-0010": {"cvssMetricV31": [metric(V31_PRIMARY, 9.8)]}},
                                  "CVE-2025-0010", "CVE-2025-9999")
        self.assertEqual(status, 1, "a requested CVE without a CVSS vector must fail the refresh")
        self.assertEqual([row["id"] for row in rows], ["CVE-2025-0010"])

    def test_bundled_catalog_is_a_builder_fixed_point(self):
        catalog = builder.OUTPUT_FILE
        self.assertTrue(catalog.is_file(), f"the builder's default output {catalog} must be the bundled catalog")
        rows = json.loads(catalog.read_text(encoding="utf-8"))
        ids = [row["id"] for row in rows]
        self.assertEqual(ids, sorted(set(ids)), "bundled catalog ids must be unique and sorted")
        for row in rows:
            self.assertEqual(set(row), CATALOG_KEYS, f"{row['id']}: bundled rows carry only catalog fields")
            self.assertEqual(row["id"], builder.normalize_id(row["id"]), f"{row['id']}: id is not normalized")
            self.assertIn(row["type"], {"CVSS_V3", "CVSS_V4"})
            self.assertTrue(row["vector"].startswith("CVSS:4.0/" if row["type"] == "CVSS_V4" else "CVSS:3."),
                            f"{row['id']}: vector {row['vector']} does not match type {row['type']}")


if __name__ == "__main__":
    unittest.main()
