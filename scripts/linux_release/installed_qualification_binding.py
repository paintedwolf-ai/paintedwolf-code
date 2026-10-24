"""Bind a server-selected raw guest campaign to independently selected control inputs."""
from __future__ import annotations

import re

from .installed_provenance import verify_producer

PRODUCER = {"format_version", "repository_id", "run_id", "run_attempt", "controller_commit", "source_commit", "source_tree"}
INPUTS = {"control_run_id", "control_artifact_id", "qualification_plan_run_id", "qualification_plan_artifact_id"}
HASHES = {"catalog_sha256", "driver_registry_sha256"}


def verify(selected: dict, document: dict, source_commit: str, source_tree: str, *,
           catalog_sha256: str, driver_registry_sha256: str, control_run_id: int,
           control_artifact_id: int, qualification_plan_run_id: int,
           qualification_plan_artifact_id: int) -> None:
    """Validate binding only; admission still executes against actual retained observations."""
    if not isinstance(document, dict) or set(document) != PRODUCER | INPUTS | HASHES:
        raise ValueError("raw qualification omitted or added controller binding fields")
    verify_producer(selected, {key: document[key] for key in PRODUCER}, source_commit, source_tree)
    expected = {"catalog_sha256": catalog_sha256, "driver_registry_sha256": driver_registry_sha256,
                "control_run_id": control_run_id, "control_artifact_id": control_artifact_id,
                "qualification_plan_run_id": qualification_plan_run_id,
                "qualification_plan_artifact_id": qualification_plan_artifact_id}
    if any(not isinstance(expected[key], str) or not re.fullmatch(r"[0-9a-f]{64}", expected[key]) for key in HASHES):
        raise ValueError("independent catalog and driver registry digests are required")
    if any(type(expected[key]) is not int or expected[key] < 1 for key in INPUTS):
        raise ValueError("independent control and qualification plan artifact IDs are required")
    if any(type(document[key]) is not type(value) or document[key] != value for key, value in expected.items()):
        raise ValueError("raw qualification differs from selected controls, plan, catalog or drivers")
