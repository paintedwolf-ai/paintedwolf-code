"""Prepare authenticated, credential-free desktop upgrade fixtures."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import urllib.request

from feed_signing import sign, verify
from release_control import capture_published, capture_sources
from release_upgrade.feeds import ORIGIN
from update_keys import generation, load_registry, validate_publication
from updater_signature import require_version

PLATFORM = "darwin-aarch64"
ARCHIVE_LIMIT = 2 * 1024 * 1024 * 1024


def archive_path(manifest: dict) -> str:
    version = manifest["version"]
    relative = f"releases/v{version}/painted-wolf-code_v{version}_{PLATFORM}.app.tar.gz"
    if manifest["platforms"][PLATFORM]["url"] != f"{ORIGIN}/{relative}":
        raise ValueError("upgrade archive must use its canonical production URL")
    return relative


def file_digest(path: Path) -> str:
    import hashlib
    value = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def fetch_archive(manifest: dict, target: Path) -> None:
    from release_distribution import PUBLIC_READ_HEADERS
    url = manifest["platforms"][PLATFORM]["url"]
    request = urllib.request.Request(url, headers=PUBLIC_READ_HEADERS)
    with urllib.request.urlopen(request, timeout=300) as response, target.open("xb") as output:
        if response.geturl() != url:
            raise ValueError("upgrade archives must not follow redirects")
        total = 0
        while block := response.read(1024 * 1024):
            total += len(block)
            if total > ARCHIVE_LIMIT:
                raise ValueError("upgrade archive exceeds its size bound")
            output.write(block)


def prepare(candidate_path: Path, archives: Path, source: Path, output: Path) -> dict:
    """Fixtures for a release candidate built from the source, whose pointer is signed here."""
    output.mkdir(parents=True, exist_ok=False)
    candidate = json.loads(candidate_path.read_text())
    if candidate["version"] != (source / "VERSION").read_text().strip():
        raise ValueError("candidate manifest differs from the selected source")
    registry = load_registry()
    validate_publication(registry, candidate, candidate["update_keys"]["signing_generation"])
    subprocess.run(["bash", str(Path(__file__).resolve().parent.parent / "release-validate-updater-manifest.sh"),
                    "--file", str(candidate_path), "--version", candidate["version"]],
                   env={**os.environ, "DOWNLOAD_BASE_URL": ORIGIN}, check=True, timeout=60)
    result = capture_sources(ORIGIN, candidate, output / "sources")
    schema = json.loads((source / "lycaon/internal/db/schema_version_lock.json").read_text())["schema_version"]
    return assemble(result, registry, archives, source, output, schema)


def prepare_published(source: Path, output: Path) -> dict:
    """Fixtures for the newest published release as the candidate, from public bytes only."""
    if os.environ.get("FEED_SIGNING_KEYS_JSON"):
        raise ValueError("published upgrade fixtures must not receive feed signing credentials")
    output.mkdir(parents=True, exist_ok=False)
    result = capture_published(ORIGIN, source, output / "sources")
    version = result["candidate"]["version"]
    schema = json.loads((source / "lycaon/testdata/upgrade-corpus" / version / "MANIFEST.json").read_text())["schema_version"]
    return assemble(result, load_registry(), None, source, output, schema)


def offer(result: dict, step: dict, channel: str, number: int, output: Path) -> tuple[Path, Path] | None:
    """The authenticated public pointer this channel already serves for the step, if any."""
    retained = next((row for row in result["feeds"] if row["channel"] == channel and row["generation"] == number
                     and row["manifest"]["version"] == step["version"]), None)
    if retained is None:
        return None
    if any(retained["manifest"][field] != step[field] for field in ("platforms", "update_keys")):
        raise ValueError("offer differs from its authenticated source pointer")
    original = output / "sources" / retained["path"]
    return original, original.with_suffix(".json.sig")


def assemble(result: dict, registry: dict, archives: Path | None, source: Path, output: Path, schema: int) -> dict:
    candidate = result["candidate"]
    manifests = {candidate["version"]: candidate}
    for case in result["cases"]:
        for manifest in [case["source_manifest"], *case["steps"]]:
            version = manifest["version"]
            if version in manifests and any(manifests[version][field] != manifest[field] for field in ("version", "platforms", "update_keys")):
                raise ValueError("one release version has conflicting manifests")
            manifests[version] = manifest
    for version, manifest in manifests.items():
        relative = archive_path(manifest)
        target = output / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        if archives is not None and version == candidate["version"]:
            shutil.copyfile(archives / Path(relative).name, target)
        else:
            fetch_archive(manifest, target)
        number = manifest["update_keys"]["signing_generation"]
        require_version(manifest["platforms"][PLATFORM]["signature"], version, "upgrade archive")
        verify(target, manifest["platforms"][PLATFORM]["signature"], generation(registry, number)["public_key"])
        manifests[version] = {**manifest, "archive_sha256": file_digest(target)}
    for anchor in result["anchors"]:
        if anchor["manifest"]["version"] not in manifests:
            raise ValueError("automatic baseline is absent from the authenticated archive inventory")
        anchor["verification"] = "artifact_signature_verified"
    result["schemas"] = {}
    for version in manifests:
        metadata = source / "lycaon/testdata/upgrade-corpus" / version / "MANIFEST.json"
        result["schemas"][version] = json.loads(metadata.read_text())["schema_version"]
    for case in result["cases"]:
        version = case["source_version"]
        fixture = output / "fixtures" / version
        if not fixture.exists():
            shutil.copytree(source / "lycaon/testdata/upgrade-corpus" / version, fixture)
        metadata = json.loads((fixture / "MANIFEST.json").read_text())
        if metadata["app_version"] != version:
            raise ValueError("prior corpus provenance differs from the source app")
        for step in case["steps"]:
            number = step["update_keys"]["signing_generation"]
            directory = output / "offers" / case["channel"] / f"key-{number}"
            directory.mkdir(parents=True, exist_ok=True)
            pointer = directory / f"latest-{case['channel']}-key-{number}.json"
            if pointer.exists():
                continue
            published = offer(result, step, case["channel"], number, output)
            if published is not None:
                shutil.copyfile(published[0], pointer)
                signature = published[1]
            elif step["version"] == candidate["version"]:
                pointer.write_text(json.dumps(step, indent=2) + "\n")
                signature = sign(pointer, number, registry)
            else:
                raise ValueError("bridge offer has no authenticated source pointer")
            shutil.copyfile(pointer, directory / "latest.json")
            shutil.copyfile(signature, directory / "latest.json.sig")
    result["source_sha"] = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
    result["schema_version"] = schema
    result["archives"] = manifests
    (output / "plan.json").write_text(json.dumps(result, indent=2) + "\n")
    inventory = {path.relative_to(output).as_posix(): file_digest(path) for path in sorted(output.rglob("*")) if path.is_file()}
    (output / "inventory.json").write_text(json.dumps(inventory, indent=2) + "\n")
    return result


def case_matrix(plan: dict) -> dict:
    rows = [{"case_id": case["id"]} for case in plan["cases"]]
    return {"include": rows or [{"case_id": "not_applicable"}]}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    candidate = parser.add_mutually_exclusive_group(required=True)
    candidate.add_argument("--manifest", type=Path, help="the release candidate's unsigned pointer")
    candidate.add_argument("--published", action="store_true", help="upgrade the published release before the newest to the newest")
    parser.add_argument("--archives", type=Path, help="the release candidate's archives")
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if (args.archives is None) != args.published:
        parser.error("--archives belongs with --manifest and only there")
    if args.published:
        result = prepare_published(args.source, args.output)
    else:
        result = prepare(args.manifest, args.archives, args.source, args.output)
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write("matrix=" + json.dumps(case_matrix(result), separators=(",", ":")) + "\n")


if __name__ == "__main__":
    main()
