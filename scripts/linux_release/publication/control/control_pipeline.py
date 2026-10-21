"""Produce nonpublishing controls and plans from independently selected native candidates."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import shutil
import tempfile

from ...installed_candidate_inputs import prepare as candidate, select as select_candidate
from ...installed_campaign import FIELDS, PLAN_FORMAT_VERSION, validate_plan
from ...installed_evidence import artifacts
from .controls import build
from ...release_admission import document
from ...release_source import facts


def plan(template, final, source, driver_tree, catalog):
    """A protected fleet template selects delivery kinds; final bytes supply identities."""
    if (not isinstance(template, dict) or set(template) != {"format_version", "targets"}
            or type(template["format_version"]) is not int or template["format_version"] != 1
            or not isinstance(template["targets"], list)):
        raise ValueError("qualification requires its exact protected fleet template")
    selected = artifacts(final, source["source_commit"])
    by_kind = {}
    for name, row in selected.items():
        key = (row["installation"], row["architecture"])
        if key in by_kind:
            raise ValueError("control bundle repeats an installation and native CPU")
        by_kind[key] = name
    targets = []
    for item in template["targets"]:
        if not isinstance(item, dict) or set(item) != FIELDS - {"artifact_id"} | {"artifact_selector"}:
            raise ValueError("fleet target differs from the fixed installed execution contract")
        selector = item["artifact_selector"]
        if not isinstance(selector, dict) or set(selector) != {"installation", "architecture"}:
            raise ValueError("fleet artifact selection requires installation and native CPU")
        key = (selector["installation"], selector["architecture"])
        if key not in by_kind:
            raise ValueError("fleet template selects an absent final installation")
        target = copy.deepcopy(item)
        del target["artifact_selector"]
        target["artifact_id"] = by_kind[key]
        targets.append(target)
    result = {"format_version": PLAN_FORMAT_VERSION, "source_commit": source["source_commit"],
              "driver_tree": driver_tree, "targets": targets}
    validate_plan(result, final, catalog)
    return result


def produce(api, repository, producer, candidate_selection, source_repository, source_commit,
            source_tree, driver_tree, destination, *, catalog, signing_catalog, private_key,
            repository_base_url, previous, nix_inputs, fleet_template):
    """Only complete immutable native producer roles can seed retained controls."""
    if (not isinstance(candidate_selection, dict)
            or set(candidate_selection) != {"run_id", "controller_commit", "artifacts"}
            or not isinstance(candidate_selection["artifacts"], dict)
            or set(candidate_selection["artifacts"]) != {"amd64", "arm64"}):
        raise ValueError("controls require exact complete native producer selections for both CPUs")
    if (destination.exists() or destination.is_symlink()
            or destination.resolve().is_relative_to(source_repository.resolve())):
        raise ValueError("control pipeline requires a fresh output outside immutable source")
    source = facts(source_repository, source_commit)
    if source["source_tree"] != source_tree:
        raise ValueError("control source differs from its selected immutable tree")
    selection = candidate_selection
    proofs = {}
    with tempfile.TemporaryDirectory(prefix=".linux-control-inputs-", dir=destination.parent) as temporary:
        working = Path(temporary)
        packages = working / "packages"
        packages.mkdir(mode=0o700)
        rows = []
        for architecture, artifact_id in selection["artifacts"].items():
            output = working / architecture
            report = candidate(api, repository, producer["repository_id"], selection["run_id"], artifact_id,
                               selection["controller_commit"], source_repository, source_commit,
                               source_tree, architecture, output, partial=False)
            proofs[architecture] = report["provenance"]
            final, _ = document(output / "final-artifacts.json")
            for name, row in artifacts(final, source_commit).items():
                if (packages / name).exists():
                    raise ValueError("native candidate identities collide across CPU roles")
                shutil.copyfile(output / "packages" / name, packages / name)
                (packages / name).chmod(0o600)
                rows.append(row)
        destination.mkdir(mode=0o700)
        try:
            controls, plans = destination / "controls", destination / "plan"
            report = build({"format_version": 1, "source_commit": source_commit, "artifacts": rows},
                           packages, controls, source, signing_catalog, private_key, repository_base_url,
                           previous, nix_inputs, producer=producer, source_repository=source_repository,
                           rehearsal=False)
            final, _ = document(controls / "final-artifacts.json")
            actual_plan = plan(fleet_template, final, source, driver_tree, catalog)
            # Re-read server-owned attempts/digests after the signing and NAR work.
            for architecture, artifact_id in selection["artifacts"].items():
                current = select_candidate(api, repository, producer["repository_id"], selection["run_id"],
                                           artifact_id, selection["controller_commit"], architecture, False)
                if current != proofs[architecture]:
                    raise ValueError("native candidate producer changed while controls were assembled")
            plans.mkdir(mode=0o700)
            proof = {"format_version": 1, **producer, "source_commit": source_commit, "source_tree": source_tree}
            for name, value in (("qualification-plan.json", actual_plan), ("plan-producer.json", proof)):
                with (plans / name).open("x") as stream:
                    json.dump(value, stream, indent=2, sort_keys=True)
                    stream.write("\n")
            return {"format_version": 1, "published": False, "application_qualified": False,
                    "source": source, "candidate_selections": proofs, "controls": report,
                    "plan_targets": len(actual_plan["targets"])}
        except BaseException:
            shutil.rmtree(destination)
            raise


def main(arguments=None):
    import argparse
    import os
    import stat
    from ...installed_provenance import controller_context
    from ..trust.generations import CATALOG as signing_catalog
    from ...support_matrix import CATALOG
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("source-commit", "source-tree", "candidate-controller-commit"):
        parser.add_argument("--" + name, required=True)
    for name in ("candidate-run-id", "candidate-amd64-artifact-id", "candidate-arm64-artifact-id"):
        parser.add_argument("--" + name, type=int, required=True)
    for name in ("source-repository", "policy", "output", "receipt"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        producer = controller_context(os.environ)
        policy_path = args.policy.absolute()
        info = policy_path.lstat()
        if (policy_path.resolve(strict=True) != policy_path or not stat.S_ISREG(info.st_mode)
                or info.st_uid not in {0, os.getuid()} or info.st_mode & 0o022 or info.st_size > 8 << 20):
            raise ValueError("control policy must be a physical owner-controlled bounded configuration")
        policy, _ = document(policy_path)
        fields = {"format_version", "private_key", "repository_base_url", "predecessors", "nix_inputs", "fleet_template"}
        if (not isinstance(policy, dict) or set(policy) != fields
                or type(policy["format_version"]) is not int or policy["format_version"] != 1
                or not isinstance(policy["private_key"], str) or not Path(policy["private_key"]).is_absolute()):
            raise ValueError("control policy must declare its exact signing, delivery, Nix and fleet inputs")
        if (args.receipt.exists() or args.receipt.is_symlink()
                or args.receipt.resolve().is_relative_to(args.output.resolve())
                or args.receipt.resolve().is_relative_to(args.source_repository.resolve())):
            raise ValueError("control receipt must be a fresh independent path")
        catalog, _ = document(CATALOG)
        controller_source = facts(Path(__file__).resolve().parents[4], producer["controller_commit"])
        from scripts.ci_policy.github import api
        report = produce(api, os.environ["GITHUB_REPOSITORY"], producer,
                         {"run_id": args.candidate_run_id, "controller_commit": args.candidate_controller_commit,
                          "artifacts": {"amd64": args.candidate_amd64_artifact_id, "arm64": args.candidate_arm64_artifact_id}},
                         args.source_repository, args.source_commit, args.source_tree, controller_source["source_tree"],
                         args.output, catalog=catalog, signing_catalog=signing_catalog,
                         private_key=Path(policy["private_key"]), repository_base_url=policy["repository_base_url"],
                         previous=policy["predecessors"], nix_inputs=policy["nix_inputs"], fleet_template=policy["fleet_template"])
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        with args.receipt.open("x") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")
        print(f"Nonpublishing controls and qualification plan retained: {args.output}")
    except (OSError, ValueError, KeyError, TypeError, ImportError) as error:
        parser.exit(1, "Linux control pipeline refused: " + str(error) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
