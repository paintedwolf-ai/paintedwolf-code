"""Admit a successful same-repository closure artifact as a private VM build input."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import platform
import re
import shutil
import subprocess

from .nix_closure import describe, nar_digest


def admit(run, manifest, report, *, source_commit, source_tree, lock_sha256, architecture, repository, controller_ref):
    """The producer is this repository's Nix qualification on the same controller ref."""
    if (run.get("conclusion") != "success" or not controller_ref or run.get("head_branch") != controller_ref
            or run.get("path") != ".github/workflows/linux-nix-qualification.yml"
            or not repository or run.get("repository", {}).get("full_name") != repository):
        raise ValueError("VM input must come from the successful repository Nix qualification workflow")
    if (architecture not in {"x86_64", "aarch64"}
            or not isinstance(run.get("head_sha"), str) or not re.fullmatch("[0-9a-f]{40}", run["head_sha"])
            or not isinstance(source_tree, str) or not re.fullmatch("[0-9a-f]{40}", source_tree)):
        raise ValueError("VM producer requires exact native source and controller identities")
    expected = {"source_commit": source_commit, "source_tree": source_tree, "lock_sha256": lock_sha256}
    if (any(manifest.get(key) != value or report.get(key) != value for key, value in expected.items())
            or manifest.get("controller_commit") != run.get("head_sha")
            or manifest.get("architecture") != architecture or manifest.get("execution") != "native"
            or manifest.get("publication") != "nonrelease" or report.get("passed") is not True
            or report.get("component") != "default" or report.get("archive", {}).get("publication") != "nonrelease"):
        raise ValueError("retained application provenance differs from the selected native source")
    paths = [path for build in report.get("builds", []) for path in build.get("outputs", {}).values()]
    if len(paths) != 1:
        raise ValueError("retained application requires one selected output")
    describe(report.get("closure"), paths, source_commit, lock_sha256)
    return paths[0]


def nix_hash(digest):
    alphabet = "0123456789abcdfghijklmnpqrsvwxyz"
    number = int.from_bytes(digest, "little")
    return "sha256:" + "".join(alphabet[(number >> (5 * index)) & 31] for index in range(51, -1, -1))


def validate_cache(cache, closure):
    info = cache / "nix-cache-info"
    if cache.is_symlink() or not cache.is_dir():
        raise ValueError("retained cache must be an owned directory")
    entries = list(cache.rglob("*"))
    if any(path.is_symlink() or not (path.is_file() or path.is_dir()) for path in entries):
        raise ValueError("retained cache must contain only regular files and directories")
    if (not info.is_file() or info.stat().st_size > 4096
            or info.read_text().splitlines().count("StoreDir: /nix/store") != 1):
        raise ValueError("retained cache must use the standard local Nix store")
    keys = []
    for line in info.read_text().splitlines():
        key, separator, value = line.partition(": ")
        if (not separator or key in keys or key not in {"StoreDir", "WantMassQuery", "Priority"}
                or (key != "StoreDir" and not value.isdigit())):
            raise ValueError("retained cache has ambiguous store configuration")
        keys.append(key)
    expected = {"nix-cache-info"}
    for path, row in closure.items():
        name = path.removeprefix("/nix/store/").split("-", 1)[0] + ".narinfo"
        metadata = cache / name
        if metadata.is_symlink() or metadata.stat().st_size > 65536:
            raise ValueError("retained cache metadata exceeds its regular-file boundary")
        fields = {}
        for line in metadata.read_text().splitlines():
            key, separator, value = line.partition(": ")
            if not separator or key in fields:
                raise ValueError("retained cache metadata is malformed")
            fields[key] = value
        if (fields.get("StorePath") != path or fields.get("NarHash") != nix_hash(bytes.fromhex(nar_digest(row["narHash"])))
                or fields.get("NarSize") != str(row["narSize"])
                or sorted(fields.get("References", "").split()) != sorted(Path(ref).name for ref in row["references"])):
            raise ValueError("retained cache metadata differs from its application closure")
        url = fields.get("URL", "")
        if not re.fullmatch(r"nar/[0-9a-z]+\.nar\.(xz|zst|bz2)", url):
            raise ValueError("retained cache NAR must be an owned relative compressed object")
        payload = cache / url
        if payload.is_symlink() or not payload.is_file() or payload.stat().st_size != int(fields.get("FileSize", "-1")):
            raise ValueError("retained cache NAR file differs from its metadata")
        digest = hashlib.sha256()
        with payload.open("rb") as stream:
            for block in iter(lambda: stream.read(8 << 20), b""):
                digest.update(block)
        if fields.get("FileHash") != nix_hash(digest.digest()):
            raise ValueError("retained compressed NAR does not match its file digest")
        # Nix itself checks the uncompressed NAR hash during substitution. The
        # admission boundary first fixes metadata and disallows unrelated paths.
        expected.update((name, url))
    actual = {str(path.relative_to(cache)) for path in entries if path.is_file()}
    if actual != expected:
        raise ValueError("retained cache contains undeclared objects or links")


def admit_retained(producer: Path, cache: Path, run: Path, evidence: Path, root: Path, environment) -> Path:
    """Admit the producer's retained closure for the checkout at root; returns the cache to trust."""
    reports = [json.loads(path.read_text()) for path in (producer / "nix").glob("build-*.json")]
    selected = [report for report in reports if report.get("component") == "default"]
    if len(selected) != 1:
        raise ValueError("producer must retain exactly one application build report")
    report = selected[0]
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    tree = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip()
    admit(json.loads(run.read_text()), json.loads((producer / "qualification-source.json").read_text()), report,
          source_commit=commit, source_tree=tree,
          lock_sha256=hashlib.sha256((root / "flake.lock").read_bytes()).hexdigest(),
          architecture=platform.machine(), repository=environment.get("GITHUB_REPOSITORY"),
          controller_ref=environment.get("GITHUB_REF_NAME"))
    validate_cache(cache, report["closure"])
    (evidence / "nix").mkdir(parents=True)
    (evidence / "nix" / "application-producer.json").write_text(json.dumps(report, indent=2) + "\n")
    shutil.copyfile(run, evidence / "application-producer-run.json")
    shutil.copyfile(producer / "qualification-source.json", evidence / "application-producer-source.json")
    return cache
