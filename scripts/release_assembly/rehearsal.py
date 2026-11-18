#!/usr/bin/env python3
"""Run the release's own scripts on tiny inputs signed with a disposable key; nothing is published.

Every check runs twice: the production input must pass and a seeded fault the release
once shipped must be refused, so a check that stops refusing is as red as one that fails.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import copy
import functools
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import feed_signing  # noqa: E402
from release_assembly.artifact_limits import check as check_limits  # noqa: E402
from release_distribution_contract.platforms import detached_signatures, load_platforms  # noqa: E402

CATALOG = ROOT / "packaging/release-platforms.json"
APPLICATION = Path("/Applications/Painted Wolf Code.app")
ORIGIN = "https://downloads.paintedwolf.dev"


class Refused(Exception):
    """A seeded fault was refused, as it must be."""


def run(arguments, **kwargs) -> subprocess.CompletedProcess:
    return subprocess.run(arguments, check=True, capture_output=True, text=True, timeout=600, **kwargs)


def refused(label: str, action) -> str:
    """Run a seeded fault; return the refusal, or raise when the fault was admitted."""
    try:
        action()
    except (ValueError, OSError, subprocess.CalledProcessError, tarfile.TarError) as error:
        detail = error.stderr.strip() if isinstance(error, subprocess.CalledProcessError) and error.stderr else str(error)
        return detail.splitlines()[-1] if detail else type(error).__name__
    raise ValueError(f"the rehearsal admitted its seeded fault: {label}")


class Key:
    """A disposable minisign key; signatures are prehashed, as the updater requires."""

    def __init__(self, directory: Path):
        directory.mkdir(parents=True)
        self.secret, public = directory / "rehearsal.key", directory / "rehearsal.pub"
        run(["minisign", "-G", "-W", "-p", str(public), "-s", str(self.secret)])
        self.public = base64.b64encode(public.read_bytes()).decode()

    def sign(self, path: Path, version: str) -> str:
        signature = path.with_name(path.name + ".minisig")
        run(["minisign", "-S", "-s", str(self.secret), "-m", str(path), "-x", str(signature),
             "-t", f"timestamp:0\tfile:{path.name}\tversion:{version}"])
        document = base64.b64encode(signature.read_bytes()).decode()
        signature.unlink()
        return document


def version() -> str:
    return (ROOT / "VERSION").read_text().strip()


def updater_archive(path: Path, release: str) -> Path:
    """An application bundle archive holding only its product version resource."""
    payload = (release + "\n").encode()
    with tarfile.open(path, "w:gz") as archive:
        for name in ("Painted Wolf Code.app", "Painted Wolf Code.app/Contents", "Painted Wolf Code.app/Contents/Resources"):
            entry = tarfile.TarInfo(name)
            entry.type, entry.mode = tarfile.DIRTYPE, 0o755
            archive.addfile(entry)
        entry = tarfile.TarInfo("Painted Wolf Code.app/Contents/Resources/release-version")
        entry.size, entry.mode = len(payload), 0o644
        archive.addfile(entry, io.BytesIO(payload))
    return path


def platform_results(directory: Path, release: str, key: Key) -> Path:
    """One signed platform result per public catalog row, shaped as staging leaves them."""
    directory.mkdir(parents=True)
    (directory / "opengrep").mkdir()
    bindings = json.loads(run([sys.executable, str(ROOT / "scripts/release-metadata.py")]).stdout)
    keys = {field: bindings[field] for field in ("signing_generation", "embedded_generation",
                                                 "signing_key_fingerprint", "embedded_key_fingerprint")}
    for row in load_platforms(CATALOG).values():
        if row["publication"] != "public":
            continue
        stem = f"painted-wolf-code_v{release}_{row['updater_key']}"
        for extension in row["package_extensions"]:
            package = directory / f"{stem}.{extension}"
            package.write_bytes(f"tiny {extension} payload\n".encode())
            (directory / f"{package.name}.sha256").write_text(hashlib.sha256(package.read_bytes()).hexdigest() + "\n")
        for extension in detached_signatures(row):
            (directory / f"{stem}.{extension}.sig").write_text("disposable detached signature\n")
        (directory / "opengrep" / f"{stem}.json").write_text("{}\n")
        updater = row.get("updater_extension")
        if updater is None:
            continue
        artifact = updater_archive(directory / f"{stem}.{updater}", release)
        signature = key.sign(artifact, release)
        (directory / f"{artifact.name}.sig").write_text(signature)
        (directory / f"fragment-{row['updater_key']}.json").write_text(json.dumps(
            {"platform": row["updater_key"], "signature": signature, "updater_artifact": artifact.name, "update_keys": keys}))
    return directory


def check_workflows() -> dict:
    """No workflow sets a name the runner overwrites (1.0.2-rc3 shipped one in a step)."""
    from toolchain import python_tools
    import workflow_lint
    sys.path.insert(0, str(python_tools.provision(workflow_lint.REQUIREMENTS)))
    import yaml
    for path in sorted((ROOT / ".github/workflows").glob("*.yml")):
        workflow_lint.environment(yaml.safe_load(path.read_text()))
    release = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
    seeded = copy.deepcopy(release)
    step = next(step for job in seeded["jobs"].values() for step in job.get("steps", []) if "run" in step)
    step.setdefault("env", {})["RUNNER_NAME"] = "${{ matrix.runner }}"
    return {"production": "passed", "seeded_reserved_name": refused("RUNNER_NAME in a release step",
                                                                    lambda: workflow_lint.environment(seeded))}


def check_limits_and_assembly(work: Path, key) -> dict:
    """Upload limits, then manifest, public set and cask assembly with the release's aggregate script."""
    release = version()
    results = platform_results(work / "release-artifacts", release, key)
    darwin = results / f"painted-wolf-code_v{release}_darwin-aarch64.app.tar.gz"
    check_limits(darwin, release, "darwin-aarch64")
    limits = json.loads((ROOT / "packaging/update-limits.json").read_text())
    oversized = work / "oversized" / darwin.name
    oversized.parent.mkdir()
    with oversized.open("wb") as sparse:
        sparse.truncate(limits["max_download_bytes"] + 1)
    receipt = {"limits": {"production": "passed", "seeded_over_upload_limit": refused(
        "an updater archive over the download limit", lambda: check_limits(oversized, release, "darwin-aarch64"))}}

    def aggregate(artifacts: Path, output: Path):
        run(["bash", str(ROOT / "scripts/release_assembly/aggregate.sh"), "--artifacts", str(artifacts), "--output", str(output / "aggregate"),
             "--public", str(output / "public"), "--version", release, "--channel", "stable", "--cask-token", "painted-wolf-code"],
            cwd=ROOT, env={**os.environ, "DOWNLOAD_BASE_URL": ORIGIN})

    aggregate(results, work / "assembled")
    manifest = json.loads((work / "assembled/aggregate/latest.json").read_text())
    for entry in manifest["platforms"].values():
        artifact = work / "assembled/public" / entry["url"].rsplit("/", 1)[1]
        feed_signing.verify(artifact, entry["signature"], key.public)
    casks = sorted(path.name for path in (work / "assembled/aggregate").glob("*.rb"))
    if casks != ["painted-wolf-code.rb", "painted-wolf-code@preview.rb"]:
        raise ValueError(f"a stable release renders both casks, not {casks}")
    mismatched = work / "mismatched"
    shutil.copytree(results, mismatched)
    fragment = mismatched / "fragment-darwin-aarch64.json"
    rebound = json.loads(fragment.read_text())
    rebound["signature"] = key.sign(darwin, "0.0.1")
    fragment.write_text(json.dumps(rebound))
    receipt["assembly"] = {"production": "passed", "platforms": sorted(manifest["platforms"]), "casks": casks,
                           "seeded_signature_for_another_version": refused(
                               "an updater signature bound to another version",
                               lambda: aggregate(mismatched, work / "refused"))}
    return receipt


def assembly(work: Path, key) -> dict:
    return {"workflows": check_workflows(), **check_limits_and_assembly(work, key)}


class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *_):
        pass


@contextlib.contextmanager
def origin(root: Path):
    handler = functools.partial(QuietHandler, directory=str(root))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_address[1]}"
    finally:
        server.shutdown()
        thread.join()
        server.server_close()


def require_disposable_runner() -> None:
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise ValueError("the cask install rehearsal changes /Applications and needs a disposable hosted runner")
    if APPLICATION.exists() or (Path.home() / ".config/paintedwolf").exists():
        raise ValueError("the cask install rehearsal needs a runner without the product installed")


def install_cask(dmg: Path, release: str, work: Path) -> dict:
    """Install and uninstall the preview cask rendered from the template against the built disk image."""
    require_disposable_runner()
    served = work / "origin" / "releases" / f"v{release}"
    served.mkdir(parents=True)
    shutil.copyfile(dmg, served / f"painted-wolf-code_v{release}_darwin-aarch64.dmg")
    environment = {**os.environ, "HOMEBREW_NO_AUTO_UPDATE": "1", "HOMEBREW_NO_ANALYTICS": "1"}
    tap, token = "paintedwolf/rehearsal", "painted-wolf-code@preview"
    application, marker = APPLICATION, Path.home() / ".config/paintedwolf/install-source.json"
    with origin(work / "origin") as base:
        run(["brew", "tap-new", "--no-git", tap], env=environment)
        try:
            directory = Path(run(["brew", "--repository", tap], env=environment).stdout.strip()) / "Casks"
            directory.mkdir(exist_ok=True)
            run(["bash", str(ROOT / "scripts/release_assembly/render-cask.sh"), "--token", token, "--channel", "preview",
                 "--version", release, "--sha256", hashlib.sha256(dmg.read_bytes()).hexdigest(),
                 "--base-url", base, "--output", str(directory / f"{token}.rb")])
            run(["brew", "install", "--cask", f"{tap}/{token}"], env=environment)
            installed = plistlib.loads((application / "Contents/Info.plist").read_bytes())["CFBundleShortVersionString"]
            recorded = json.loads(marker.read_text())
            if recorded != {"install_source": "homebrew_cask", "release_channel": "preview"} or marker.stat().st_mode & 0o777 != 0o600:
                raise ValueError("the cask did not record a private preview install marker")
            run(["brew", "uninstall", "--cask", f"{tap}/{token}"], env=environment)
            if application.exists() or marker.exists():
                raise ValueError("uninstalling the cask left the app or its install marker")
        finally:
            run(["brew", "untap", "--force", tap], env=environment)
    return {"token": token, "installed_version": installed, "uninstalled": True}


def macos(work: Path, app: Path, dmg: Path, public_key: str) -> dict:
    """The unsigned bundle's updater archive meets the limits and its disposable signature verifies; its cask installs."""
    release = version()
    updater = next(app.parent.glob("*.app.tar.gz"))
    check_limits(updater, release, "darwin-aarch64")
    public = work / "public.key"
    public.parent.mkdir(parents=True, exist_ok=True)
    public.write_text(public_key + "\n")
    run(["cargo", "run", "--quiet", "--locked", "--manifest-path", str(ROOT / "lycaon-den/src-tauri/Cargo.toml"),
         "--example", "verify_updater_signature", "--", str(updater), str(updater) + ".sig", str(public), release])
    return {"limits": "passed", "updater_signature": "verified", "cask": install_cask(dmg, release, work)}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    stages = parser.add_subparsers(dest="stage", required=True)
    stages.add_parser("assembly", help="workflow names, upload limits and release assembly on tiny platform results")
    bundle = stages.add_parser("macos", help="the unsigned bundle from den:bundle, then the cask install")
    bundle.add_argument("--app", type=Path, required=True)
    bundle.add_argument("--dmg", type=Path, required=True)
    bundle.add_argument("--public-key", required=True, help="the disposable updater public key, as the registry encodes keys")
    for stage in stages.choices.values():
        stage.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    if args.receipt.exists() or args.receipt.is_symlink():
        parser.error("receipt must be a fresh path")
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    receipt = {"format_version": 1, "stage": args.stage, "published": False, "result": "failed"}
    try:
        with tempfile.TemporaryDirectory(prefix="paintedwolf-release-rehearsal-") as directory:
            work = Path(directory)
            if args.stage == "assembly":
                receipt["checks"] = assembly(work, Key(work / "key"))
            else:
                receipt["checks"] = macos(work, args.app.resolve(), args.dmg.resolve(), args.public_key)
        receipt["result"] = "passed"
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else ""
        receipt["error"] = f"{error}\n{detail}".strip()
        parser.exit(1, f"release rehearsal failed: {receipt['error']}\n")
    finally:
        args.receipt.write_text(json.dumps(receipt, indent=2) + "\n")


if __name__ == "__main__":
    main()
