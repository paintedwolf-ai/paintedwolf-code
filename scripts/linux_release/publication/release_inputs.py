"""Select independently produced catalog, updater and non-Linux release bytes."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import re

ARTIFACT = "release-distribution-inputs"
WORKFLOW = ".github/workflows/release.yml"


def select(api, repository: str, repository_id: int, run_id: int, artifact_id: int) -> dict:
    """Require a successful protected producer before interpreting its archive."""
    from ..installed_provenance import artifact_for_run
    if (not isinstance(repository, str) or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository)
            or any(type(value) is not int or value < 1 for value in (repository_id, run_id, artifact_id))):
        raise ValueError("release inputs require exact repository, run and artifact identities")
    run = api(f"repos/{repository}/actions/runs/{run_id}")
    commit, attempt = run.get("head_sha"), run.get("run_attempt")
    if (type(run.get("id")) is not int or run["id"] != run_id
            or run.get("repository", {}).get("id") != repository_id
            or run.get("repository", {}).get("full_name") != repository
            or run.get("head_repository", {}).get("id") != repository_id
            or run.get("path") != WORKFLOW or run.get("head_branch") != "main"
            or run.get("event") != "workflow_dispatch" or run.get("status") != "completed"
            or run.get("conclusion") != "success" or type(attempt) is not int or attempt < 1
            or not isinstance(commit, str) or not re.fullmatch(r"[a-f0-9]{40}", commit)):
        raise ValueError("release inputs require a successful protected main producer")
    artifact = artifact_for_run(api, repository, repository_id, run_id, commit, ARTIFACT)
    if artifact["artifact_id"] != artifact_id:
        raise ValueError("release inputs differ from the independently selected artifact ID")
    return {"repository": repository, "repository_id": repository_id, "run_id": run_id,
            "run_attempt": attempt, "controller_commit": commit, **artifact}


def metadata(root: Path, provenance: dict, source: dict) -> tuple[dict, dict, dict]:
    from ..installed_provenance import verify_producer
    from scripts.release_distribution_contract.document import snapshot
    from scripts.release_distribution_contract.platforms import parse_platforms
    producer_identity, producer_raw = snapshot(root / "producer.json", retain=True, limit=65536)
    verify_producer(provenance, json.loads(producer_raw), source["source_commit"], source["source_tree"])
    catalog_identity, catalog_raw = snapshot(root / "catalog.json", retain=True, limit=2 << 20)
    updater_identity, updater_raw = snapshot(root / "updater.json", retain=True, limit=2 << 20)
    catalog, updater = parse_platforms(catalog_raw), json.loads(updater_raw)
    if (not isinstance(updater, dict) or updater.get("version") != source["version"]
            or "withdrawn" in updater or not isinstance(updater.get("platforms"), dict)
            or not isinstance(updater.get("notes"), str) or not updater["notes"].strip()
            or not isinstance(updater.get("pub_date"), str)):
        raise ValueError("retained updater differs from the immutable release source")
    return catalog, updater, {"producer": producer_identity, "catalog": catalog_identity, "updater": updater_identity}


def inventory(root: Path, catalog: dict, updater: dict, source: dict) -> dict:
    from scripts.release_distribution_contract.document import snapshot
    expected, packages, updater_platforms = set(), {}, set()
    for platform, row in catalog.items():
        if row["publication"] != "public" or platform.startswith("linux-"):
            continue
        stem = f"painted-wolf-code_v{source['version']}_{platform}"
        packages[platform] = {}
        for extension in row["package_extensions"]:
            name = f"{stem}.{extension}"
            expected.update((name, name + ".sha256"))
            identity, _ = snapshot(root / "artifacts" / name)
            _, raw = snapshot(root / "artifacts" / (name + ".sha256"), retain=True, limit=4096)
            if raw.decode().strip() != identity["sha256"]:
                raise ValueError("retained package checksum does not describe the selected bytes")
            packages[platform][extension] = {"file": name, **identity}
        extension = row["updater_extension"]
        if extension is None:
            raise ValueError("non-Linux public release platform omitted its updater")
        updater_platforms.add(platform)
        name = f"{stem}.{extension}"
        expected.update((name, name + ".sig"))
        snapshot(root / "artifacts" / name)
        _, signature = snapshot(root / "artifacts" / (name + ".sig"), retain=True, limit=65536)
        entry = updater["platforms"].get(platform)
        if (not isinstance(entry, dict) or set(entry) != {"url", "signature"}
                or entry["signature"] != signature.decode().strip()):
            raise ValueError("retained updater signature differs from its protected platform result")
    directory = root / "artifacts"
    if (directory.is_symlink() or not directory.is_dir()
            or set(path.name for path in directory.iterdir()) != expected
            or set(updater["platforms"]) != updater_platforms):
        raise ValueError("retained release inventory has missing or unexpected non-Linux artifacts")
    files = {name: snapshot(directory / name)[0] for name in sorted(expected)}
    return {"packages": packages, "files": files}


class SelectedReleaseInputs:
    """Return scoped facts; this owner grants no Linux or delivery availability authority."""

    def __init__(self, root: Path, provenance: dict, source: dict, controller_commit: str):
        if not isinstance(controller_commit, str) or not re.fullmatch(r"[a-f0-9]{40}", controller_commit):
            raise ValueError("release inputs require the current independently selected controller")
        self.root, self.provenance, self.source = root, copy.deepcopy(provenance), copy.deepcopy(source)
        self.controller_commit = controller_commit
        catalog, updater, identities = metadata(root, self.provenance, self.source)
        self.identities = copy.deepcopy(identities)
        self.inventory = inventory(root, catalog, updater, self.source)

    def authorize(self, binding: dict) -> dict:
        from scripts.release_distribution_contract.document import snapshot
        catalog, updater, identities = metadata(self.root, self.provenance, self.source)
        observed = inventory(self.root, catalog, updater, self.source)
        if identities != self.identities or observed != self.inventory:
            raise ValueError("retained release inputs changed after digest-bound selection")
        packages = observed["packages"]
        document = binding["document"]
        if (document["controller_commit"] != self.controller_commit
                or document["source"]["commit"] != self.source["source_commit"]
                or document["source"]["tree"] != self.source["source_tree"]
                or document["version"] != self.source["version"]
                or any(document[name] != updater[name] for name in ("notes", "pub_date"))):
            raise ValueError("distribution differs from independently selected release inputs")
        for field, path, expected in (("catalog_sha256", binding["catalog"], identities["catalog"]),
                                      ("updater_manifest_sha256", binding["updater_manifest"], identities["updater"])):
            actual, _ = snapshot(path, limit=2 << 20)
            announced = document["source"][field] if field == "catalog_sha256" else document[field]
            if actual != expected or announced != expected["sha256"]:
                raise ValueError("distribution metadata changed after independent release selection")
        non_linux = {key: row for key, row in document["platforms"].items() if not key.startswith("linux-")}
        if set(non_linux) != set(packages):
            raise ValueError("distribution changed the independently produced non-Linux platform set")
        for platform, rows in packages.items():
            if set(non_linux[platform]["packages"]) != set(rows):
                raise ValueError("distribution changed the non-Linux package set")
            for extension, identity in rows.items():
                declared = non_linux[platform]["packages"][extension]
                actual, _ = snapshot(binding["artifacts"] / identity["file"])
                if actual != {key: identity[key] for key in ("sha256", "bytes")} or any(
                        declared.get(key) != value for key, value in identity.items()):
                    raise ValueError("distribution non-Linux package changed after protected production")
        for platform, entry in updater["platforms"].items():
            extension = catalog[platform]["updater_extension"]
            name = f"painted-wolf-code_v{self.source['version']}_{platform}.{extension}"
            if entry["url"] != f"{document['origin']}/releases/v{self.source['version']}/{name}":
                raise ValueError("updater URL differs from the independently bound release origin")
        return {"provenance": copy.deepcopy(self.provenance), "metadata": identities,
                "non_linux_packages": packages}


def retain(api, repository: str, repository_id: int, run_id: int, artifact_id: int,
           destination: Path, source: dict, controller_commit: str) -> SelectedReleaseInputs:
    from ..installed_artifact_bundle import Limits, download
    selected = select(api, repository, repository_id, run_id, artifact_id)
    download(selected, destination, limits=Limits(max_files=1024))
    return SelectedReleaseInputs(destination, selected, source, controller_commit)
