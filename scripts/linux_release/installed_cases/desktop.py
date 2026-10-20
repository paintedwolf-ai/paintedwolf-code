"""Actual installed desktop interactions; missing drivers remain unverified."""
from __future__ import annotations

from pathlib import Path

from .app import InstalledApp
from .records import retain


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    inputs = binding.get("case_inputs", {}).get("desktop", {})
    if inputs not in ({}, {"format_version": 1}):
        raise ValueError("desktop driver does not accept arbitrary fixture or command inputs")
    if (capability, case) != ("installation", "clean-install-and-launch"):
        return retain(binding, capability, case, root, "unverified",
                      {"reason": "native-desktop-interaction-driver-required"})
    app = InstalledApp(binding)
    try:
        with app:
            response = app.request("GET", "/health")
            if response["status"] != 200:
                raise ValueError("selected installed application engine is not healthy")
    finally:
        app.stop()
    complete = app.observations.get("capture_complete") and not app.observations.get("output_limit")
    app.observations["remaining_interaction"] = "fresh-install-transaction-not-yet-observed"
    return retain(binding, capability, case, root, "unverified" if complete else "failed",
                  app.observations, app.streams())
