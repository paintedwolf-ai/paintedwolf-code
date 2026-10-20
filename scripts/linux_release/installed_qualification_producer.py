"""Prepare independently selected controls, then retain an actually executed guest campaign."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from .installed_capacity import limits as campaign_limits
from .installed_artifact_bundle import Limits, download, retain_tree
from .installed_execution import current_tree, read, registry
from .installed_campaign import validate_plan, verify_execution_receipt
from .installed_provenance import controller_context, select_control, select_plan, verify_producer
from .installed_results import admit
from .support_matrix import CATALOG

ROOT = Path(__file__).resolve().parents[2]
CONTROL_FILES = {"control-producer.json", "final-artifacts.json", "repositories.json"}
CONTROL_DIRS = {"packages", "generations", "nix-cache", "repository-setup"}
PLAN_FILES = {"plan-producer.json", "qualification-plan.json"}


def canonical(document):
    return (json.dumps(document, sort_keys=True, separators=(",", ":")) + "\n").encode()


def api(path):
    result = subprocess.run(["gh", "api", path], capture_output=True, timeout=180)
    if result.returncode or len(result.stdout) > 16 << 20:
        raise ValueError("cannot independently select the protected controller artifact")
    return json.loads(result.stdout)


def context():
    selected = controller_context(os.environ)
    expected_tree = subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", selected["controller_commit"] + "^{tree}"], text=True).strip()
    tree = current_tree(ROOT)
    if tree != expected_tree:
        raise ValueError("qualification must execute its immutable protected controller source tree")
    catalog = read(CATALOG)
    drivers = {"format_version": 1, "installations": {item["id"]: registry(catalog, item["id"]) for item in catalog["installations"]}}
    return selected, tree, catalog, canonical(drivers)


def validate_inputs(root, selections, source, tree):
    controls, plan = root / "controls", root / "plan"
    if ({path.name for path in controls.iterdir() if path.is_file()} != CONTROL_FILES
            or {path.name for path in controls.iterdir() if path.is_dir()} != CONTROL_DIRS
            or {path.name for path in plan.iterdir()} != PLAN_FILES):
        raise ValueError("selected controls or plan contain missing or unexpected root entries")
    verify_producer(selections["controls"], read(controls / "control-producer.json"), source["source_commit"], source["source_tree"])
    verify_producer(selections["plan"], read(plan / "plan-producer.json"), source["source_commit"], source["source_tree"])
    document = read(plan / "qualification-plan.json")
    selected_source, selected_tree, _ = validate_plan(document, read(controls / "final-artifacts.json"))
    if selected_source != source["source_commit"] or selected_tree != tree:
        raise ValueError("protected qualification plan differs from selected source or fixed drivers")
    return document


def prepare(args):
    producer, tree, _, drivers = context()
    repository = os.environ["GITHUB_REPOSITORY"]
    selections = {
        "controls": select_control(api, repository, producer["repository_id"], args.control_run_id, args.control_artifact_id),
        "plan": select_plan(api, repository, producer["repository_id"], args.qualification_plan_run_id, args.qualification_plan_artifact_id)}
    source = {"source_commit": args.source_commit, "source_tree": args.source_tree}
    capacity = campaign_limits(catalog, final)
    bounds = Limits(**{key: capacity[key] for key in ("max_files", "max_bytes", "max_file")})
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    try:
        download(selections["controls"], args.output / "controls")
        download(selections["plan"], args.output / "plan")
        validate_inputs(args.output, selections, source, tree)
        document = {"format_version": 1, "producer": producer, "driver_tree": tree, **source,
                    "selections": selections, "catalog_sha256": hashlib.sha256(CATALOG.read_bytes()).hexdigest(),
                    "driver_registry_sha256": hashlib.sha256(drivers).hexdigest()}
        (args.output / "preparation.json").write_bytes(canonical(document))
    except BaseException:
        shutil.rmtree(args.output)
        raise


def retain(args):
    producer, tree, catalog, drivers = context()
    preparation = read(args.prepared / "preparation.json")
    expected = {"format_version", "producer", "driver_tree", "source_commit", "source_tree", "selections", "catalog_sha256", "driver_registry_sha256"}
    if (not isinstance(preparation, dict) or set(preparation) != expected or type(preparation["format_version"]) is not int
            or preparation["format_version"] != 1 or preparation["producer"] != producer or preparation["driver_tree"] != tree
            or preparation["catalog_sha256"] != hashlib.sha256(CATALOG.read_bytes()).hexdigest()
            or preparation["driver_registry_sha256"] != hashlib.sha256(drivers).hexdigest()):
        raise ValueError("qualification preparation changed from its protected controller attempt")
    repository = os.environ["GITHUB_REPOSITORY"]
    controls, plan = preparation["selections"]["controls"], preparation["selections"]["plan"]
    selected = {
        "controls": select_control(api, repository, producer["repository_id"], controls["run_id"], controls["artifact_id"]),
        "plan": select_plan(api, repository, producer["repository_id"], plan["run_id"], plan["artifact_id"])}
    if selected != preparation["selections"]:
        raise ValueError("qualification artifacts changed from independent server selection")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-qualification-retain-") as temporary:
        fresh = Path(temporary)
        download(selected["controls"], fresh / "controls")
        download(selected["plan"], fresh / "plan")
        retain_verified(args, producer, tree, catalog, drivers, preparation, fresh)


def retain_verified(args, producer, tree, catalog, drivers, preparation, fresh):
    plan = validate_inputs(fresh, preparation["selections"], preparation, tree)
    if read(args.execution / "plan.json") != plan or (args.execution / "driver-registry.json").read_bytes() != drivers:
        raise ValueError("executed campaign changed the selected plan or fixed driver registry")
    for target in plan["targets"]:
        verify_execution_receipt(args.execution / target["id"],target,preparation["source_commit"],tree)
    final = read(fresh / "controls/final-artifacts.json")
    campaign = read(args.execution / "campaign.json")
    report = admit(catalog, campaign, final, preparation["source_commit"], args.execution / "evidence")
    if report.get("accepted") is not True or read(args.execution / "admission.json") != report:
        raise ValueError("only a complete freshly admitted actual installed campaign may be retained")
    for target in plan["targets"]:
        directory = args.execution / target["id"]
        required = ("managed-execution.log", "transfer.log", "leaf.zip", "leaf/campaign.json", "leaf/binding.json", "leaf/environment.json", "leaf/variant.json", "leaf/driver-registry.json", "leaf/execution.json", "leaf/environment-facts.json", "execution-request.json")
        if target.get('input_adapter') is not None:
            required += ('native-input.jsonl', 'native-input-transport.log')
        for name in required:
            path = directory / name
            if not path.is_file() or path.is_symlink():
                raise ValueError("raw qualification omitted a selected guest execution receipt")
    capacity = campaign_limits(catalog, final)
    bounds = Limits(**{key: capacity[key] for key in ("max_files", "max_bytes", "max_file")})
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    try:
        retain_tree(fresh / "controls", args.output / "control-inputs", root_files=CONTROL_FILES, root_dirs=CONTROL_DIRS)
        targets = {target["id"] for target in plan["targets"]}
        retain_tree(args.execution, args.output / "installed-campaign",
                    root_files={"campaign.json", "admission.json", "plan.json", "driver-registry.json"}, root_dirs={"evidence", *targets}, limits=bounds)
        controls, selected_plan = preparation["selections"]["controls"], preparation["selections"]["plan"]
        document = {"format_version": 1, **producer, "source_commit": preparation["source_commit"], "source_tree": preparation["source_tree"],
                    "catalog_sha256": preparation["catalog_sha256"], "driver_registry_sha256": preparation["driver_registry_sha256"],
                    "control_run_id": controls["run_id"], "control_artifact_id": controls["artifact_id"],
                    "qualification_plan_run_id": selected_plan["run_id"], "qualification_plan_artifact_id": selected_plan["artifact_id"]}
        (args.output / "qualification.json").write_bytes(canonical(document))
    except BaseException:
        shutil.rmtree(args.output)
        raise


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="operation", required=True)
    prepare_parser = commands.add_parser("prepare")
    for name in ("source-commit", "source-tree"):
        prepare_parser.add_argument("--" + name, required=True)
    for name in ("control-run-id", "control-artifact-id", "qualification-plan-run-id", "qualification-plan-artifact-id"):
        prepare_parser.add_argument("--" + name, type=int, required=True)
    retain_parser = commands.add_parser("retain")
    retain_parser.add_argument("--prepared", type=Path, required=True)
    retain_parser.add_argument("--execution", type=Path, required=True)
    for command in (prepare_parser, retain_parser):
        command.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        prepare(args) if args.operation == "prepare" else retain(args)
    except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, str(error) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
