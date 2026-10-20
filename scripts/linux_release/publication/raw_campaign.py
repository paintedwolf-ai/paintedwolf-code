"""Compose final admission inputs from a protected, actually executed raw campaign."""
from __future__ import annotations

import hashlib
from pathlib import Path
import re

from ..installed_artifact_bundle import Limits, download, retain_tree
from ..installed_capacity import limits as campaign_limits
from ..installed_campaign import validate_plan, verify_execution_receipt
from ..installed_execution import registry
from ..installed_provenance import select_control, select_plan, select_qualification, verify_producer
from ..installed_qualification_binding import verify as verify_binding
from ..installed_qualification_producer import CONTROL_FILES, CONTROL_DIRS, PLAN_FILES, canonical
from ..release_admission import document
from ..support_matrix import CATALOG


def materialize(api, repository: str, repository_id: int, run_id: int, source: dict,
                catalog: dict, directory: Path) -> tuple[Path, dict]:
    raw_selection = select_qualification(api, repository, repository_id, run_id)
    raw = directory / "raw"
    capacity = campaign_limits(catalog)
    transport_bounds = Limits(**{key: capacity[key] for key in ("max_files", "max_bytes", "max_file")})
    download(raw_selection, raw, limits=transport_bounds)
    if ({path.name for path in raw.iterdir() if path.is_file()} != {"qualification.json"}
            or {path.name for path in raw.iterdir() if path.is_dir()} != {"control-inputs", "installed-campaign"}):
        raise ValueError("raw qualification contains missing or unexpected producer roots")
    qualification, _ = document(raw / "qualification.json")
    if not isinstance(qualification, dict):
        raise ValueError("raw qualification binding must be an object")
    controls = select_control(api, repository, repository_id, qualification.get("control_run_id"), qualification.get("control_artifact_id"))
    plan_selection = select_plan(api, repository, repository_id, qualification.get("qualification_plan_run_id"), qualification.get("qualification_plan_artifact_id"))
    drivers = canonical({"format_version": 1, "installations": {item["id"]: registry(catalog, item["id"]) for item in catalog["installations"]}})
    verify_binding(raw_selection, qualification, source["source_commit"], source["source_tree"],
                   catalog_sha256=hashlib.sha256(CATALOG.read_bytes()).hexdigest(),
                   driver_registry_sha256=hashlib.sha256(drivers).hexdigest(),
                   control_run_id=controls["run_id"], control_artifact_id=controls["artifact_id"],
                   qualification_plan_run_id=plan_selection["run_id"], qualification_plan_artifact_id=plan_selection["artifact_id"])
    fresh, plan_root = directory / "controls", directory / "plan"
    download(controls, fresh)
    download(plan_selection, plan_root)
    if ({path.name for path in fresh.iterdir() if path.is_file()} != CONTROL_FILES
            or {path.name for path in fresh.iterdir() if path.is_dir()} != CONTROL_DIRS
            or {path.name for path in plan_root.iterdir()} != PLAN_FILES):
        raise ValueError("selected controls or plan have changed their fixed role roots")
    verify_producer(controls, document(fresh / "control-producer.json")[0], source["source_commit"], source["source_tree"])
    verify_producer(plan_selection, document(plan_root / "plan-producer.json")[0], source["source_commit"], source["source_tree"])
    plan, _ = document(plan_root / "qualification-plan.json")
    final, _ = document(fresh / "final-artifacts.json")
    selected_source, driver_tree, targets = validate_plan(plan, final)
    capacity = campaign_limits(catalog, final)
    execution_bounds = Limits(**{key: capacity[key] for key in ("max_files", "max_bytes", "max_file")})
    commit = api(f"repos/{repository}/git/commits/{raw_selection['controller_commit']}")
    expected_tree = commit.get("tree", {}).get("sha")
    if (selected_source != source["source_commit"] or not isinstance(expected_tree, str)
            or not re.fullmatch(r"[0-9a-f]{40}", expected_tree) or driver_tree != expected_tree):
        raise ValueError("raw guest plan differs from its immutable protected driver implementation")
    executed = raw / "installed-campaign"
    if document(executed / "plan.json")[0] != plan or (executed / "driver-registry.json").read_bytes() != drivers:
        raise ValueError("raw observations differ from the selected plan or fixed driver registry")
    expected_files = {"campaign.json", "admission.json", "plan.json", "driver-registry.json"}
    expected_dirs = {"evidence", *[target["id"] for target in targets]}
    if ({path.name for path in executed.iterdir() if path.is_file()} != expected_files
            or {path.name for path in executed.iterdir() if path.is_dir()} != expected_dirs):
        raise ValueError("raw campaign omitted a selected guest execution directory")
    for target in targets:
        verify_execution_receipt(executed / target["id"],target,source["source_commit"],driver_tree)
    retained = directory / "executed"
    retain_tree(executed, retained, root_files={"campaign.json", "admission.json", "plan.json", "driver-registry.json"},
                root_dirs={"evidence", *[target["id"] for target in targets]}, limits=execution_bounds)
    inputs = directory / "inputs"
    retain_tree(fresh, inputs, root_files=CONTROL_FILES, root_dirs=CONTROL_DIRS)
    (inputs / "control-producer.json").unlink()
    (retained / "campaign.json").rename(inputs / "campaign.json")
    (retained / "evidence").rename(inputs / "evidence")
    return inputs, {"format_version": 1, "qualification": raw_selection, "controls": controls,
                    "plan": plan_selection, "binding": qualification}
