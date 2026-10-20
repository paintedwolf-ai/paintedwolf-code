"""Actual installed desktop interactions; missing drivers remain unverified."""
from __future__ import annotations

from pathlib import Path
import json

from .. import installed_evidence as evidence

from .app import InstalledApp
from .desktop_ui import interactive_window
from .records import retain


def installation_transaction(binding: dict, root: Path) -> list[dict] | None:
    transaction = binding.get("installation_transaction")
    if transaction is None:
        return None
    row = evidence.object_fields(transaction, {"format_version", "status", "source_commit", "artifact_id", "evidence", "observations"})
    if (type(row["format_version"]) is not int or row["format_version"] != 1 or row["status"] != "passed"
            or row["source_commit"] != binding["source_commit"] or row["artifact_id"] != binding["artifact"]["id"]
            or not isinstance(row["evidence"], list) or not 1 <= len(row["evidence"]) <= 17):
        raise ValueError("fresh installation transaction differs from the selected installed artifact")
    journal, seen = None, set()
    for record in row["evidence"]:
        evidence.retained_file(record, root)
        if record["path"] in seen:
            raise ValueError("fresh installation evidence repeats a retained observation")
        seen.add(record["path"])
        if not record["path"].startswith("installation/preinstallation-transaction/"):
            raise ValueError("fresh installation evidence belongs to a different operation")
        if record["path"] == "installation/preinstallation-transaction/execution.json":
            path = root / record["path"]
            if path.stat().st_size > 1 << 20:
                raise ValueError("fresh installation journal exceeds its bound")
            journal = json.loads(path.read_text())
    expected = {"format_version": 1, "source_commit": binding["source_commit"], "artifact": binding["artifact"],
                "capability": "installation", "case": "preinstallation-transaction", "status": "passed",
                "observations": row["observations"]}
    observations = row["observations"]
    if (journal != expected or not isinstance(observations, dict)
            or observations.get("operation") != "fresh-install" or observations.get("expected_absent") is not True
            or observations.get("profile_absent") is not True or observations.get("installed") is not True
            or observations.get("recovery_required") is not False
            or observations.get("output_limit") is not False):
        raise ValueError("fresh installation journal did not observe successful absent-to-installed effects")
    return row["evidence"]


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    inputs = binding.get("case_inputs", {}).get("desktop", {})
    if inputs not in ({}, {"format_version": 1}):
        raise ValueError("desktop driver does not accept arbitrary fixture or command inputs")
    if (capability, case) != ("installation", "clean-install-and-launch"):
        return retain(binding, capability, case, root, "unverified",
                      {"reason": "native-desktop-interaction-driver-required"})
    transaction = installation_transaction(binding, root)
    app = InstalledApp(binding)
    try:
        with app:
            app.observations["interactive_window"] = interactive_window(app)
            response = app.request("GET", "/health")
            if response["status"] != 200:
                raise ValueError("selected installed application engine is not healthy")
    finally:
        app.stop()
    complete = app.observations.get("capture_complete") and not app.observations.get("output_limit")
    if transaction is None:
        app.observations["remaining_interaction"] = "fresh-install-transaction-not-yet-observed"
    result = retain(binding, capability, case, root,
                    "passed" if complete and transaction is not None else "unverified" if complete else "failed",
                    app.observations, app.streams())
    if transaction is not None:
        result["evidence"].extend(transaction)
    return result
