"""Assemble and publish the release's signed Linux package repositories in the shared release lane."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import tempfile
from urllib.parse import urlsplit

from release_distribution_contract.platforms import load_platforms, ORIGIN

from . import (native_repositories, pacman_publication, pacman_repository, pacman_r2, release_qualification,
               repository_r2, repository_transaction)
from .r2_objects import current, public_bytes, put, prefix as checked_prefix
from .repository_objects import encoded
from .repository_operations import identity, version_marker
from .release_signing import private_key
from .signing import CATALOG, digest
from .openpgp import selection

ROOT = Path(__file__).resolve().parents[2]
PLATFORMS = ROOT / "packaging/release-platforms.json"
STORAGE_PREFIX = "linux"
PACKAGE = "painted-wolf-code"
FAMILIES = {"deb": "deb", "rpm": "rpm", "pacman": "pkg.tar.zst"}


def publication_configuration(catalog, storage_prefix, base_url, rehearsal):
    if type(rehearsal) is not bool:
        raise ValueError("repository trust mode must be explicit")
    checked_prefix(storage_prefix, rehearsal)
    selected = urlsplit(base_url)
    if (selected.scheme != "https" or not selected.netloc or selected.path or selected.query or selected.fragment
            or selected.username or selected.password):
        raise ValueError("repository publication requires its selected HTTPS origin")
    if rehearsal:
        if catalog == CATALOG or selected.hostname == urlsplit(ORIGIN).hostname:
            raise ValueError("rehearsal publication requires disposable trust and isolated origin")
    elif catalog != CATALOG or storage_prefix != STORAGE_PREFIX or base_url != ORIGIN:
        raise ValueError("production publication requires maintained trust, origin and namespace")
    selection(catalog, rehearsal)


def public_linux(catalog: Path | None = None) -> list[dict]:
    return [row for row in load_platforms(catalog or PLATFORMS).values() if row["publication"] == "public" and row["goos"] == "linux"]


def public_packages(artifacts: Path, version: str, family: str, catalog: Path | None = None) -> dict[str, dict]:
    """Every catalog-public Linux package of one family, bound to its published checksum."""
    result = {}
    for row in public_linux(catalog):
        name = f"painted-wolf-code_v{version}_{row['updater_key']}.{FAMILIES[family]}"
        path = artifacts / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"public Linux package missing: {name}")
        value = digest(path)
        if (artifacts / (name + ".sha256")).read_text().strip() != value:
            raise ValueError(f"public Linux package differs from its published checksum: {name}")
        result[name] = {"path": path, "sha256": value, "goarch": row["goarch"], "architecture": row["artifact_arch"]}
    return result


def native_version(family: str, path: Path) -> str:
    if family == "deb":
        arguments = ["dpkg-deb", "-f", str(path), "Version"]
    elif family == "rpm":
        arguments = ["rpm", "-qp", "--nosignature", "--qf", "%{VERSION}-%{RELEASE}", str(path)]
    else:
        return pacman_repository.package_info(path)["pkgver"]
    return subprocess.check_output(arguments, text=True, timeout=60).strip()


def plan(artifacts: Path, version: str, source_commit: str, catalog: Path | None = None, *, read_version=None,
         families=frozenset(FAMILIES)) -> list[dict]:
    """One apt and one dnf generation for every architecture, one pacman generation per architecture."""
    read_version = read_version or native_version
    generations = []
    if not public_linux(catalog):
        return generations
    for family in [name for name in ["deb", "rpm"] if name in families]:
        rows = [{"file": name, "sha256": row["sha256"], "architecture": row["goarch"],
                 "native_version": read_version(family, row["path"])}
                for name, row in sorted(public_packages(artifacts, version, family, catalog).items())]
        generations.append({"name": family, "family": family, "manifest": {
            "format_version": 1, "source_commit": source_commit, "version": version, "packages": rows}})
    pacman = public_packages(artifacts, version, "pacman", catalog) if "pacman" in families else {}
    for name, row in sorted(pacman.items()):
        generations.append({"name": "pacman-" + row["architecture"], "family": "pacman", "manifest": {
            "format_version": 1, "source_commit": source_commit, "version": version,
            "architecture": row["architecture"], "packages": [
                {"file": name, "sha256": row["sha256"], "native_version": read_version("pacman", row["path"])}]}})
    # Each offer is paired with the signed empty generation a halt activates when no earlier
    # Linux release exists on that channel, or when the halt names no replacement.
    for row in list(generations):
        architectures = None if row["family"] == "pacman" else sorted(
            {item["architecture"] for item in row["manifest"]["packages"]})
        generations.append({"name": row["name"] + "-withdrawal", "family": row["family"], "architectures": architectures,
                            "manifest": {**row["manifest"], "packages": []}})
    return generations


def assemble(families: set[str], artifacts: Path, output: Path, version: str, source_commit: str, channels: list[str],
             private: Path, catalog: Path = CATALOG, platforms: Path | None = None, *, rehearsal=False) -> list[dict]:
    if type(rehearsal) is not bool:
        raise ValueError("repository trust mode must be explicit")
    output.mkdir(parents=True, exist_ok=False)
    selected = plan(artifacts, version, source_commit, platforms, families=families)
    index = []
    for channel in channels:
        for row in selected:
            name = channel + "/" + row["name"]
            destination = output / name
            destination.parent.mkdir(exist_ok=True)
            if row["family"] == "pacman":
                receipt = pacman_repository.assemble(row["manifest"], artifacts, destination, channel, catalog, private, rehearsal)
            else:
                receipt = native_repositories.assemble(row["family"], row["manifest"], artifacts, destination,
                                                       channel, catalog, private, rehearsal=rehearsal, architectures=row.get("architectures"))
            (destination / "generation.json").write_text(json.dumps(receipt, indent=2) + "\n")
            index.append({"name": name, "family": row["family"]})
    (output / "generations.json").write_text(json.dumps(index, indent=2) + "\n")
    return index


def withdrawal_key(receipt: dict, *, storage_prefix=STORAGE_PREFIX) -> str:
    """Where a release retains the empty generation that withdraws its offer on one channel."""
    return f"{storage_prefix}/withdrawal-generations/{version_marker(receipt).relative_to('withdrawn-versions').as_posix()}"


def channel_key(receipt: dict, *, storage_prefix=STORAGE_PREFIX) -> str:
    channel = f"channels/{receipt['family']}/{receipt['channel']}"
    if receipt["family"] == "pacman":
        channel += "/" + receipt["architecture"]
    return f"{storage_prefix}/{channel}/current.json"


class ReleaseAuthority:
    """Fresh shared-qualification admission and exact binding to this run's public Linux files."""

    def __init__(self, artifacts: Path, version: str, source_commit: str, channel: str, base_url: str,
                 qualified, platforms: Path | None = None, *, rehearsal=False, catalog=None, storage_prefix=STORAGE_PREFIX):
        if type(rehearsal) is not bool:
            raise ValueError("repository trust mode must be explicit")
        self.rehearsal, self.catalog, self.storage_prefix = rehearsal, catalog or CATALOG, storage_prefix
        self.artifacts, self.version, self.source_commit, self.channel = artifacts, version, source_commit, channel
        self.base_url, self.qualified, self.platforms = base_url, qualified, platforms

    def expected_packages(self, manifest: dict) -> dict[str, str]:
        family = manifest["family"]
        public = public_packages(self.artifacts, self.version, family, self.platforms)
        if family == "pacman":
            public = {name: row for name, row in public.items() if row["architecture"] == manifest["architecture"]}
        return {name: row["sha256"] for name, row in public.items()}

    def __call__(self, request: dict) -> bool:
        publication_configuration(self.catalog, self.storage_prefix, self.base_url, self.rehearsal)
        if (request["catalog"] != self.catalog or request["storage_prefix"] != self.storage_prefix
                or request["base_url"] != self.base_url or request["operation"] != "publish"):
            raise ValueError("Linux repository publication differs from the release lane's production target")
        self.check(request["manifest"], rehearsal=self.rehearsal)
        if not callable(self.qualified):
            raise ValueError("repository publication requires fresh source admission")
        self.qualified(self.source_commit)
        return True

    def check(self, manifest: dict, *, rehearsal: bool = False):
        if type(rehearsal) is not bool:
            raise ValueError("repository trust mode must be explicit")
        if (manifest.get("rehearsal") is not rehearsal or manifest.get("source_commit") != self.source_commit
                or manifest.get("version") != self.version or manifest.get("channel") != self.channel):
            raise ValueError("Linux repository generation differs from the selected release")
        selected = {Path(row["file"]).name: row["sha256"] for row in manifest["packages"]}
        if not selected or selected != self.expected_packages(manifest):
            raise ValueError("Linux repository packages differ from this release's signed public packages")


def retain_withdrawal(receipt: dict, offer: dict, directory: Path, base_url: str, qualified, *,
                      upload=put, verify=public_bytes, rehearsal=False, catalog=None, storage_prefix=STORAGE_PREFIX, transport=None) -> str:
    """Retain the signed empty generation before its offer activates, so a halt never needs signing."""
    catalog = catalog or CATALOG
    publication_configuration(catalog, storage_prefix, base_url, rehearsal)
    if rehearsal and transport is None:
        raise ValueError("rehearsal retention requires its explicit isolated transport")
    if transport is not None:
        upload, verify = transport.put, transport.public_bytes
    if (receipt.get("packages") != [] or receipt.get("rehearsal") is not rehearsal
            or any(receipt.get(key) != offer.get(key) for key in
                   ["family", "channel", "version", "source_commit", "architecture", "signing_generation", "key_fingerprint"])):
        raise ValueError("Linux withdrawal generation differs from the offer it withdraws")
    qualified(receipt["source_commit"])
    retain_generation(receipt, directory, base_url, catalog, storage_prefix, rehearsal, upload, verify)
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-withdrawal-") as temporary:
        pointer = Path(temporary) / "withdrawal.json"
        pointer.write_bytes(encoded(receipt))
        upload(withdrawal_key(receipt, storage_prefix=storage_prefix), pointer)
        verify(f"{base_url.rstrip('/')}/{withdrawal_key(receipt, storage_prefix=storage_prefix)}", pointer)
    return identity(receipt)


def retain_generation(receipt: dict, directory: Path, base_url: str, catalog: Path, storage_prefix: str, rehearsal: bool,
                      upload, verify):
    """Upload one admitted generation's immutable objects and manifest before anything references it."""
    origin = base_url.rstrip("/") + "/" + storage_prefix
    local = pacman_publication.publish if receipt["family"] == "pacman" else repository_transaction.publish
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-retention-") as temporary:
        store = Path(temporary) / "origin"
        local(receipt, directory, store, origin, catalog, None, rehearsal)
        generation = store / "generations" / identity(receipt)
        for source in sorted(generation.rglob("*")):
            if source.is_file():
                key = f"{storage_prefix}/{source.relative_to(store).as_posix()}"
                upload(key, source)
                verify(f"{base_url.rstrip('/')}/{key}", source)


def publish(generations: list[Path], artifacts: Path, version: str, source_commit: str, channels: list[str], base_url: str,
            qualified, *, read_current=current, publishers=None, retain=retain_withdrawal, rehearsal=False,
            catalog=None, storage_prefix=STORAGE_PREFIX, transport=None) -> list[dict]:
    catalog = catalog or CATALOG
    publication_configuration(catalog, storage_prefix, base_url, rehearsal)
    if rehearsal and transport is None:
        raise ValueError("rehearsal publication requires its explicit isolated transport")
    if transport is not None:
        read_current = transport.current
    publishers = publishers or {"deb": repository_r2.publish_r2, "rpm": repository_r2.publish_r2,
                                "pacman": pacman_r2.publish_r2}
    selected = [{**row, "directory": root / row["name"]} for root in generations
                for row in json.loads((root / "generations.json").read_text())]
    expected_names = {channel + "/" + row["name"] for channel in channels
                      for row in plan(artifacts, version, source_commit, read_version=lambda *_: "")}
    if sorted(row["name"] for row in selected) != sorted(expected_names):
        raise ValueError("assembled Linux repositories differ from the catalog-public Linux platforms")
    results = []
    loaded = {row["name"]: json.loads((row["directory"] / "generation.json").read_text()) for row in selected}
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-repositories-") as temporary:
        for row in selected:
            if row["name"].endswith("-withdrawal"):
                continue
            directory = row["directory"]
            receipt = loaded[row["name"]]
            if receipt.get("family") != row["family"]:
                raise ValueError("assembled Linux repository index differs from its generation")
            channel = row["name"].split("/")[0]
            authority = ReleaseAuthority(artifacts, version, source_commit, channel, base_url, qualified, rehearsal=rehearsal, catalog=catalog, storage_prefix=storage_prefix)
            authority.check(receipt, rehearsal=rehearsal)
            companion = next(item for item in selected if item["name"] == row["name"] + "-withdrawal")
            retain(loaded[companion["name"]], receipt, companion["directory"], base_url, qualified,
                   rehearsal=rehearsal, catalog=catalog, storage_prefix=storage_prefix, transport=transport)
            active = read_current(channel_key(receipt, storage_prefix=storage_prefix), Path(temporary))
            expected = None if active is None else active["generation"]
            results.append(publishers[row["family"]](receipt, directory, catalog, storage_prefix, base_url, expected,
                                                     rehearsal, "publish", "activate", authorize=authority,
                                                     **({"transport": transport} if transport is not None else {})))
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ["assemble", "publish"]:
        command = commands.add_parser(name)
        command.add_argument("--artifacts", type=Path, required=True)
        command.add_argument("--version", required=True)
        command.add_argument("--source-commit", required=True)
        command.add_argument("--channel", choices=["stable", "preview"], required=True)
        command.add_argument("--plan", type=Path, required=True)
    commands.choices["assemble"].add_argument("--family", choices=["native", "pacman"], required=True)
    commands.choices["assemble"].add_argument("--output", type=Path, required=True)
    commands.choices["publish"].add_argument("--generations", type=Path, action="append", required=True)
    commands.choices["publish"].add_argument("--base-url", required=True)
    commands.choices["publish"].add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        from release_distribution_contract.channels import validate, check
        from update_keys import load_registry
        selected = validate(json.loads(args.plan.read_bytes()), args.source_commit, args.version, args.channel,
                            load_registry()["signing_generation"])
        if args.command == "assemble":
            families = {"deb", "rpm"} if args.family == "native" else {"pacman"}
            if not public_linux():
                args.output.mkdir(parents=True, exist_ok=False)
                (args.output / "generations.json").write_text("[]\n")
                return
            with private_key() as private:
                assemble(families, args.artifacts, args.output, args.version, args.source_commit, selected["channels"], private)
        else:
            def admit(source):
                check(selected)
                release_qualification.require(source)
            result = publish(args.generations, args.artifacts, args.version, args.source_commit,
                             selected["channels"], args.base_url.rstrip("/"), admit)
            args.output.write_text(json.dumps(result, indent=2) + "\n")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux release repositories failed: {exc}\n")


if __name__ == "__main__":
    main()
