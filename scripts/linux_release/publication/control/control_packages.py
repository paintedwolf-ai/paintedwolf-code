"""Build native control inputs from exact candidate files without repository activation."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import shutil
import tempfile

from ...installed_evidence import artifacts
from ..trust.repositories import digest as generation_digest
from ...signing import digest
from scripts.release_semver import pacman_package_version, parse, parse_release_build

FAMILIES = {"deb", "rpm", "pacman"}
ARCHITECTURES = {"amd64": "x86_64", "arm64": "aarch64"}


def stage(document, inputs, destination, source, families, architectures):
    selected = copy.deepcopy(artifacts(document, source["source_commit"]))
    if (not families or not set(families) <= FAMILIES or not architectures
            or not set(architectures) <= set(ARCHITECTURES)):
        raise ValueError("control inputs require explicit native families and architectures")
    if any(row["installation"] not in families or row["architecture"] not in architectures
           or row["identity"]["kind"] != "file" for row in selected.values()):
        raise ValueError("native input manifest contains an undeclared family, CPU or nonfile artifact")
    wanted = {(family, architecture) for family in families for architecture in architectures}
    observed = [(row["installation"], row["architecture"]) for row in selected.values()]
    if set(observed) != wanted or len(observed) != len(wanted):
        raise ValueError("native inputs must contain exactly one package for every selected family and CPU")
    destination.mkdir()
    for name, row in selected.items():
        path = inputs / name
        if path.is_symlink() or not path.is_file() or digest(path) != row["identity"]["sha256"]:
            raise ValueError("candidate package differs from its selected immutable bytes")
        copied = destination / name
        shutil.copyfile(path, copied)
        copied.chmod(0o600)
        if digest(copied) != row["identity"]["sha256"]:
            raise ValueError("candidate package changed during control retention")
    return selected


def regular_aliases(directory, manifest):
    """Archive signed pacman alias bytes, preserving their admitted object identities."""
    indexed = {row["path"]: row for row in manifest["objects"]}
    for path in directory.iterdir():
        if not path.is_symlink():
            continue
        target = path.resolve(strict=True)
        row = indexed.get(path.name)
        if target.parent != directory.resolve() or row is None or digest(target) != row["sha256"] or target.stat().st_size != row["size"]:
            raise ValueError("signed repository alias escaped or changed its admitted object")
        raw = target.read_bytes()
        path.unlink()
        with path.open("xb") as output:
            output.write(raw)
        path.chmod(0o600)


def assemble(selected, root, source, catalog, private, base_url, previous, *, rehearsal):
    from ...candidate_repositories import package_row
    from ...native_repositories import assemble as native
    from ...repository_operations import transition
    from ...repository_setup import build as setup
    from ...repository_validation import admit as native_admit
    plan, channels = [], set()
    for family in sorted({row["installation"] for row in selected.values()}):
        rows = [row for row in selected.values() if row["installation"] == family]
        groups = [[row for row in rows if row["architecture"] == architecture] for architecture in sorted({row["architecture"] for row in rows})] if family == "pacman" else [rows]
        for group in groups:
            architecture = ARCHITECTURES[group[0]["architecture"]] if family == "pacman" else None
            channel = family + ("/" + architecture if architecture else "")
            channels.add(channel)
            if not rehearsal and channel not in previous:
                raise ValueError("production controls require an explicit independently selected predecessor for every channel")
            manifest = {"format_version": 1, "source_commit": source["source_commit"], "version": source["version"]}
            with tempfile.TemporaryDirectory(prefix="paintedwolf-control-generation-") as temporary:
                directory = Path(temporary) / "generation"
                if family == "pacman":
                    from ...pacman_repository import assemble as pacman, package_info
                    from ...pacman_validation import admit as pacman_admit
                    packages = []
                    for row in group:
                        path = root / "packages" / row["id"]
                        version = pacman_package_version(parse(source["version"])) + "-" + str(parse_release_build(str(source["release_build"])))
                        if package_info(path) != {"pkgname":"painted-wolf-code", "pkgver":version, "arch":architecture}:
                            raise ValueError("pacman candidate differs from the independently selected product identity")
                        packages.append({"file": path.name, "sha256": digest(path), "architecture": architecture, "native_version": version})
                    manifest.update(architecture=architecture, packages=packages)
                    receipt = pacman(manifest, root / "packages", directory, source["channel"], catalog, private, rehearsal)
                    pacman_admit(receipt, directory, catalog, rehearsal)
                    regular_aliases(directory, receipt)
                    pacman_admit(receipt, directory, catalog, rehearsal)
                else:
                    manifest["packages"] = [package_row(root / "packages" / row["id"], family, "painted-wolf-code", source["version"]) for row in group]
                    receipt = native(family, manifest, root / "packages", directory, source["channel"], catalog, private, rehearsal)
                    native_admit(receipt, directory, catalog, rehearsal)
                generation = generation_digest(receipt)
                retained = root / "generations" / generation
                shutil.copytree(directory, retained)
            (retained / "generation.json").write_text(json.dumps(receipt, sort_keys=True, indent=2) + "\n")
            for row in receipt["packages"]:
                name = Path(row["file"]).name
                shutil.copyfile(retained / row["file"], root / "packages" / name)
                (root / "packages" / name).chmod(0o600)
                selected[name]["identity"]["sha256"] = row["sha256"]
            current = previous.get(channel)
            operation = transition(receipt, current, None if current is None else current["generation"])
            plan.append({"manifest": receipt, "operation": operation})
            if family != "pacman":
                setup(receipt, retained, catalog, root / "repository-setup" / generation, base_url, private, rehearsal)
    if set(previous) - channels:
        raise ValueError("predecessor selection contains an undeclared repository channel")
    return {"format_version": 1, "generations": plan}
