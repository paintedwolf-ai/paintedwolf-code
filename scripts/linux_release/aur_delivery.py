"""Bind AUR recipe availability separately from binary repository publication."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import os
import shlex
import stat
import subprocess
import tempfile

from scripts.release_semver import compare, parse
from .aur_recipe import generate
from .repository_objects import encoded
from .repository_operations import identity

RECIPE_FILES = {"PKGBUILD", ".SRCINFO", "painted-wolf-code.install"}


def recipe_objects(directory: Path) -> list[dict]:
    names = {path.name for path in directory.iterdir() if path.name in RECIPE_FILES}
    if not {"PKGBUILD", ".SRCINFO"}.issubset(names):
        raise ValueError("AUR delivery requires both generated recipe declarations")
    rows = []
    for name in sorted(names):
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError("AUR recipe declarations must be regular files")
        raw = path.read_bytes()
        if len(raw) > 1 << 20:
            raise ValueError("AUR recipe declaration exceeds its size bound")
        rows.append({"path": name, "sha256": hashlib.sha256(raw).hexdigest(), "size": len(raw)})
    return rows


def plan(metadata: dict, generations: list[dict], directory: Path, final_artifacts_sha256: str) -> dict:
    if not re.fullmatch(r"[0-9a-f]{64}", final_artifacts_sha256):
        raise ValueError("AUR delivery requires the selected final-artifact inventory identity")
    if not re.fullmatch(r"[a-z0-9][a-z0-9+._-]*", metadata["package"]):
        raise ValueError("AUR package name is invalid")
    if not generations or any(row.get("family") != "pacman" or any(
            row.get(name) != metadata[name] for name in ["source_commit", "version", "channel", "rehearsal"])
            for row in generations):
        raise ValueError("AUR delivery differs from its selected binary generations")
    objects = recipe_objects(directory)
    return {"format_version": 1, "operation": "publish-aur", "source_commit": metadata["source_commit"],
            "version": metadata["version"], "channel": metadata["channel"], "package": metadata["package"],
            "rehearsal": metadata["rehearsal"], "final_artifacts_sha256": final_artifacts_sha256,
            "generations": [{"generation": identity(row), "inventory_sha256": row["inventory_sha256"],
                             "architecture": row["architecture"], "signing_generation": row["signing_generation"],
                             "key_fingerprint": row["key_fingerprint"]} for row in generations],
            "recipe_objects": objects, "recipe_sha256": hashlib.sha256(encoded(objects)).hexdigest()}


def production_publish(*_args):
    # A caller-supplied accepted JSON document is not protected workflow provenance.
    raise ValueError("AUR publication requires protected controller reevaluation and explicit publish-aur authorization")


class RecipePublisher:
    """Publish only after the protected caller reevaluates its selected inputs."""

    def __init__(self, root: Path, origin: str, authorize, environment=None):
        self.root, self.origin, self.authorize = root, origin, authorize
        selected_environment = os.environ if environment is None else environment
        self.environment = {key: value for key, value in selected_environment.items()
                            if not key.startswith("GIT_") or key == "GIT_SSH_COMMAND"}
        self.environment.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null", GIT_TERMINAL_PROMPT="0")
        self.sequence = 0

    def git(self, *arguments, capture=False, binary=False):
        return subprocess.run(["git", *map(str, arguments)], env=self.environment, check=True,
                              capture_output=capture, text=capture and not binary)

    def head(self):
        raw = self.git("ls-remote", self.origin, "refs/heads/master", capture=True).stdout.strip()
        if not raw:
            return None
        if not re.fullmatch(r"[0-9a-f]{40}\trefs/heads/master", raw):
            raise ValueError("AUR origin returned an ambiguous branch identity")
        return raw.split("\t")[0]

    def publish(self, selected: dict, recipe: Path, expected: str | None):
        self.authorize(selected, recipe)
        if selected.get("operation") != "publish-aur":
            raise ValueError("AUR delivery requires its separate explicit operation")
        objects = recipe_objects(recipe)
        if objects != selected["recipe_objects"] or hashlib.sha256(encoded(objects)).hexdigest() != selected["recipe_sha256"]:
            raise ValueError("AUR recipe changed after delivery selection")
        actual = self.head()
        if actual is None and expected is not None:
            raise ValueError("AUR recipe origin changed since the reviewed delivery plan")
        self.sequence += 1
        checkout = self.root / ("checkout-" + str(self.sequence))
        self.git("clone", "--no-hardlinks", self.origin, checkout)
        if actual is not None:
            cloned = self.git("-C", checkout, "rev-parse", "HEAD", capture=True).stdout.strip()
            if cloned != actual:
                raise ValueError("AUR origin changed while its reviewed recipe was cloning")
            previous = json.loads(self.git("-C", checkout, "show", "-s", "--format=%B", actual, capture=True).stdout)
            if {key: value for key, value in previous.items() if key != "previous"} == selected:
                if expected not in {actual, previous.get("previous")}:
                    raise ValueError("AUR idempotent retry differs from its reviewed predecessor")
                self.verify_readback(checkout, objects)
                return {**previous, "commit": actual, "availability": "origin-readback"}
            if actual != expected:
                raise ValueError("AUR recipe origin changed since the reviewed delivery plan")
            if (previous["package"] != selected["package"] or previous["channel"] != selected["channel"]
                    or compare(parse(selected["version"]), parse(previous["version"])) <= 0):
                raise ValueError("ordinary AUR delivery must advance the selected package and channel")
        for name in RECIPE_FILES:
            path = checkout / name
            if path.is_symlink():
                raise ValueError("AUR recipe origin contains a symlink declaration")
            if path.exists():
                path.unlink()
        for row in objects:
            with (recipe / row["path"]).open("rb") as source:
                raw = source.read((1 << 20) + 1)
            if len(raw) != row["size"] or hashlib.sha256(raw).hexdigest() != row["sha256"]:
                raise ValueError("AUR recipe changed while its reviewed content was staging")
            (checkout / row["path"]).write_bytes(raw)
        self.git("-C", checkout, "add", "--", *sorted(row["path"] for row in objects))
        self.git("-C", checkout, "add", "--update", "--", ".")
        self.git("-C", checkout, "-c", "user.name=Painted Wolf Code release",
                 "-c", "user.email=release@paintedwolf.ai", "commit", "--allow-empty", "-m",
                 encoded({**selected, "previous": expected}).decode().strip())
        delivered = self.git("-C", checkout, "rev-parse", "HEAD", capture=True).stdout.strip()
        for row in objects:
            raw = self.git("-C", checkout, "show", "HEAD:" + row["path"], capture=True, binary=True).stdout
            if len(raw) != row["size"] or hashlib.sha256(raw).hexdigest() != row["sha256"]:
                raise ValueError("AUR committed recipe differs before publication")
        self.git("-C", checkout, "push", "origin", "HEAD:refs/heads/master")
        readback = self.root / ("readback-" + str(self.sequence))
        self.git("clone", "--no-hardlinks", self.origin, readback)
        if self.git("-C", readback, "rev-parse", "HEAD", capture=True).stdout.strip() != delivered:
            raise ValueError("AUR origin changed before delivery readback")
        self.verify_readback(readback, objects)
        return {**selected, "previous": expected, "commit": delivered, "availability": "origin-readback"}

    @staticmethod
    def verify_readback(checkout, objects):
        for row in objects:
            path = checkout / row["path"]
            if path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != row["sha256"]:
                raise ValueError("AUR origin readback differs from the selected recipe")


def protected_publisher(root: Path, package: str, private_key: Path, known_hosts: Path, authorize):
    """The controller supplies a reevaluation callback; accepted JSON is never authority."""
    if not re.fullmatch(r"painted-wolf-code(?:-preview)?-bin", package):
        raise ValueError("select the maintained AUR application package")
    for path in [private_key, known_hosts]:
        value = path.lstat()
        if not stat.S_ISREG(value.st_mode) or value.st_uid != os.getuid() or value.st_mode & 0o077:
            raise ValueError("AUR SSH identity and known hosts require owner-only regular files")
    command = ["ssh", "-F", "/dev/null", "-i", str(private_key), "-o", "BatchMode=yes",
               "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
               "-o", "UserKnownHostsFile=" + str(known_hosts)]
    environment = {**os.environ, "GIT_SSH_COMMAND": shlex.join(command), "GIT_TERMINAL_PROMPT": "0"}
    def protected_authority(selected, recipe):
        if selected.get("rehearsal") is not False or selected.get("package") != package:
            raise ValueError("AUR production delivery differs from its selected package or trust mode")
        authorize(selected, recipe)
    return RecipePublisher(root, "ssh://aur@aur.archlinux.org/" + package + ".git", protected_authority, environment)


class FixturePublisher(RecipePublisher):
    """Own only a bare Git origin created inside this invocation's temporary root."""

    def __init__(self, root: Path):
        origin = root / "aur-origin.git"
        subprocess.run(["git", "init", "--bare", "--initial-branch=master", str(origin)], check=True)
        super().__init__(root, str(origin), self.fixture_authority)

    @staticmethod
    def fixture_authority(selected, _recipe):
        if selected.get("rehearsal") is not True:
            raise ValueError("fixture origin accepts only explicitly selected rehearsal recipe delivery")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--generation", type=Path, action="append", required=True)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--final-artifacts-sha256", required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--operation", choices=["prepare", "publish-aur"], default="prepare")
    parser.add_argument("--rehearsal", action="store_true")
    args = parser.parse_args()
    try:
        if args.operation == "publish-aur":
            production_publish()
        generations = [(json.loads((path / "generation.json").read_text()), path) for path in args.generation]
        with tempfile.TemporaryDirectory(prefix="paintedwolf-aur-delivery-") as temporary:
            recipe = Path(temporary) / "recipe"
            metadata = generate(generations, recipe, args.base_url, args.catalog, args.rehearsal)
            selected = plan(metadata, [row for row, _ in generations], recipe, args.final_artifacts_sha256)
            args.receipt.parent.mkdir(parents=True, exist_ok=True)
            args.receipt.write_bytes(encoded({**selected, "availability": "prepared-only", "aur_published": False}))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"AUR delivery refused: {exc}\n")


if __name__ == "__main__":
    main()
