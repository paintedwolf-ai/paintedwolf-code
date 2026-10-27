"""Assemble and publish the release's signed Linux package repositories in the shared release lane."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from release_distribution_contract.platforms import load_platforms

from . import native_repositories, pacman_repository, pacman_r2, release_qualification, repository_r2
from .r2_objects import current
from .release_signing import private_key
from .signing import CATALOG, digest

ROOT = Path(__file__).resolve().parents[2]
PLATFORMS = ROOT / "packaging/release-platforms.json"
STORAGE_PREFIX = "linux"
PACKAGE = "painted-wolf-code"
FAMILIES = {"deb": "deb", "rpm": "rpm", "pacman": "pkg.tar.zst"}


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


def plan(artifacts: Path, version: str, source_commit: str, catalog: Path | None = None, *, read_version=None) -> list[dict]:
    """One apt and one dnf generation for every architecture, one pacman generation per architecture."""
    read_version = read_version or native_version
    generations = []
    if not public_linux(catalog):
        return generations
    for family in ["deb", "rpm"]:
        rows = [{"file": name, "sha256": row["sha256"], "architecture": row["goarch"],
                 "native_version": read_version(family, row["path"])}
                for name, row in sorted(public_packages(artifacts, version, family, catalog).items())]
        generations.append({"name": family, "family": family, "manifest": {
            "format_version": 1, "source_commit": source_commit, "version": version, "packages": rows}})
    for name, row in sorted(public_packages(artifacts, version, "pacman", catalog).items()):
        generations.append({"name": "pacman-" + row["architecture"], "family": "pacman", "manifest": {
            "format_version": 1, "source_commit": source_commit, "version": version,
            "architecture": row["architecture"], "packages": [
                {"file": name, "sha256": row["sha256"], "native_version": read_version("pacman", row["path"])}]}})
    return generations


def assemble(families: set[str], artifacts: Path, output: Path, version: str, source_commit: str, channel: str,
             private: Path, catalog: Path = CATALOG, platforms: Path | None = None) -> list[dict]:
    output.mkdir(parents=True, exist_ok=False)
    selected = [row for row in plan(artifacts, version, source_commit, platforms) if row["family"] in families]
    for row in selected:
        destination = output / row["name"]
        if row["family"] == "pacman":
            receipt = pacman_repository.assemble(row["manifest"], artifacts, destination, channel, catalog, private)
        else:
            receipt = native_repositories.assemble(row["family"], row["manifest"], artifacts, destination,
                                                   channel, catalog, private)
        (destination / "generation.json").write_text(json.dumps(receipt, indent=2) + "\n")
    (output / "generations.json").write_text(json.dumps(
        [{"name": row["name"], "family": row["family"]} for row in selected], indent=2) + "\n")
    return selected


def channel_key(receipt: dict) -> str:
    channel = f"channels/{receipt['family']}/{receipt['channel']}"
    if receipt["family"] == "pacman":
        channel += "/" + receipt["architecture"]
    return f"{STORAGE_PREFIX}/{channel}/current.json"


class ReleaseAuthority:
    """Fresh shared-qualification admission and exact binding to this run's public Linux files."""

    def __init__(self, artifacts: Path, version: str, source_commit: str, channel: str, base_url: str,
                 qualified, platforms: Path | None = None):
        self.artifacts, self.version, self.source_commit, self.channel = artifacts, version, source_commit, channel
        self.base_url, self.qualified, self.platforms = base_url, qualified, platforms

    def expected_packages(self, manifest: dict) -> dict[str, str]:
        family = manifest["family"]
        public = public_packages(self.artifacts, self.version, family, self.platforms)
        if family == "pacman":
            public = {name: row for name, row in public.items() if row["architecture"] == manifest["architecture"]}
        return {name: row["sha256"] for name, row in public.items()}

    def __call__(self, request: dict) -> bool:
        manifest = request["manifest"]
        if (request["catalog"] != CATALOG or request["storage_prefix"] != STORAGE_PREFIX
                or request["base_url"] != self.base_url or request["operation"] != "publish"):
            raise ValueError("Linux repository publication differs from the release lane's production target")
        if (manifest.get("rehearsal") is not False or manifest.get("source_commit") != self.source_commit
                or manifest.get("version") != self.version or manifest.get("channel") != self.channel):
            raise ValueError("Linux repository generation differs from the selected release")
        selected = {Path(row["file"]).name: row["sha256"] for row in manifest["packages"]}
        if not selected or selected != self.expected_packages(manifest):
            raise ValueError("Linux repository packages differ from this release's signed public packages")
        # Each authorization reads the exact-source qualification record again, as the macOS lane does.
        self.qualified(self.source_commit)
        return True


def publish(generations: list[Path], artifacts: Path, version: str, source_commit: str, channel: str, base_url: str,
            qualified, *, read_current=current, publishers=None) -> list[dict]:
    publishers = publishers or {"deb": repository_r2.publish_r2, "rpm": repository_r2.publish_r2,
                                "pacman": pacman_r2.publish_r2}
    authority = ReleaseAuthority(artifacts, version, source_commit, channel, base_url, qualified)
    selected = [{**row, "directory": root / row["name"]} for root in generations
                for row in json.loads((root / "generations.json").read_text())]
    expected_names = {row["name"] for row in plan(artifacts, version, source_commit, read_version=lambda *_: "")}
    if sorted(row["name"] for row in selected) != sorted(expected_names):
        raise ValueError("assembled Linux repositories differ from the catalog-public Linux platforms")
    results = []
    with tempfile.TemporaryDirectory(prefix="paintedwolf-linux-repositories-") as temporary:
        for row in selected:
            directory = row["directory"]
            receipt = json.loads((directory / "generation.json").read_text())
            if receipt.get("family") != row["family"]:
                raise ValueError("assembled Linux repository index differs from its generation")
            active = read_current(channel_key(receipt), Path(temporary))
            expected = None if active is None else active["generation"]
            results.append(publishers[row["family"]](receipt, directory, CATALOG, STORAGE_PREFIX, base_url, expected,
                                                     False, "publish", "activate", authorize=authority))
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
    commands.choices["assemble"].add_argument("--family", choices=["native", "pacman"], required=True)
    commands.choices["assemble"].add_argument("--output", type=Path, required=True)
    commands.choices["publish"].add_argument("--generations", type=Path, action="append", required=True)
    commands.choices["publish"].add_argument("--base-url", required=True)
    args = parser.parse_args()
    try:
        if args.command == "assemble":
            families = {"deb", "rpm"} if args.family == "native" else {"pacman"}
            if not public_linux():
                args.output.mkdir(parents=True, exist_ok=False)
                (args.output / "generations.json").write_text("[]\n")
                return
            with private_key() as private:
                assemble(families, args.artifacts, args.output, args.version, args.source_commit, args.channel, private)
        else:
            print(json.dumps(publish(args.generations, args.artifacts, args.version, args.source_commit,
                                     args.channel, args.base_url.rstrip("/"), release_qualification.require), indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Linux release repositories failed: {exc}\n")


if __name__ == "__main__":
    main()
