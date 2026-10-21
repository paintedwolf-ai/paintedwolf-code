"""Run pinned Nix evaluation/build operations inside normal verification admission."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import time
import tempfile

from ..artifact_paths import artifact_root
from . import nix_archive, factory_source, nix_runner, nix_scanner_tools

ROOT = Path(__file__).resolve().parents[2]
SYSTEMS = {"x86_64": "x86_64-linux", "aarch64": "aarch64-linux"}
NIX = ["nix", "--extra-experimental-features", "nix-command flakes"]
CREDENTIALS = ("GH_TOKEN", "GITHUB_TOKEN", "LINUX_PACKAGE_SIGNING_KEY", "FEED_SIGNING_KEYS_JSON",
               "TAURI_SIGNING_PRIVATE_KEY", "TAURI_SIGNING_PRIVATE_KEY_PASSWORD")
COMPONENTS = ("default", "bun-lock", "frontend", "shell", "engine", "bound-engine", "bialy", "document-core",
              "resources", "browser", "gitengine", "ocr", "decision-resources", "scanner-original", "scanner-finalized", "notices", "notice-tools", "closure-notices",
              "scanner-host-tools", "nixos-module", "nixos-gnome-vm", "nixos-plasma-vm", "nix-workflow-fixture")


def credential_free_environment() -> dict:
    """Nix evaluation and builds never receive publication or repository credentials."""
    return {name: value for name, value in os.environ.items() if name not in CREDENTIALS}


def command(operation: str, root: Path, component: str = "default", source: str | None = None) -> list[str]:
    source = source or "git+file://" + str(root)
    if operation == "check":
        return NIX + ["flake", "check", "--no-build", "--all-systems", "--no-write-lock-file", source]
    if platform.system() != "Linux" or platform.machine() not in SYSTEMS:
        raise ValueError("native Nix package builds require Linux amd64 or arm64")
    system = SYSTEMS[platform.machine()]
    section = "checks" if component == "nixos-module" else "packages"
    return NIX + ["build", "--json", "--no-link", "--no-write-lock-file",
                  "--option", "sandbox", "true", "--max-jobs", "1", "--cores", "1",
                  source + "#" + section + "." + system + "." + component]


def execute(operation: str, root: Path = ROOT, component: str = "default",
            archive_dir: Path | None = None, factory_root: Path | None = None,
            factory_commit: str | None = None, factory_channel: str | None = None,
            prepare_factory_lock: bool = False, application_root: Path | None = None,
            application_commit: str | None = None) -> int:
    scanner_source = None
    if application_root is not None or application_commit is not None:
        if component != "scanner-host-tools" or application_root is None or application_commit is None:
            raise ValueError("Explicit application input is restricted to scanner host-tools builds")
        if not application_root.is_absolute() or application_root.resolve() != application_root:
            raise ValueError("Application input must be a physical absolute checkout")
        import re
        if not re.fullmatch("[0-9a-f]{40}", application_commit):
            raise ValueError("Application input requires a full immutable commit")
        if subprocess.check_output(["git", "rev-parse", "--show-toplevel"], cwd=application_root, text=True).strip() != str(application_root):
            raise ValueError("Application input must name the Git root")
        scanner_source = "git+file://" + str(application_root) + "?rev=" + application_commit
    role = None
    if any(value is not None for value in (factory_root, factory_commit, factory_channel)):
        if not all(value is not None for value in (factory_root, factory_commit, factory_channel)):
            raise ValueError("Factory root, commit and channel must be supplied together")
        role = factory_source.select(factory_root, factory_commit, factory_channel, component,
                                     prepare=prepare_factory_lock)
    if prepare_factory_lock and (role is None or operation != "check" or archive_dir is not None):
        raise ValueError("Factory lock preparation requires check and an explicit factory input")
    lock = root / "flake.lock"
    if not lock.is_file():
        raise ValueError("flake.lock is required; evaluation/build must not create or update it")
    if archive_dir is not None and operation != "build":
        raise ValueError("closure archives require a build operation")
    archive = nix_archive.destination(root, archive_dir)
    if archive is not None and role is not None and archive.is_relative_to(factory_root):
        raise ValueError("Factory archive must stay outside the factory source tree")
    source = None if role is None else ("git+file://" + str(factory_root) + "?rev=" +
                                       factory_commit + "&dir=" + role["subdirectory"])
    invocation = command(operation, root, component, source)
    environment = credential_free_environment()
    report = {
        "format_version": 1, "operation": operation, "component": component,
        "lock_sha256": hashlib.sha256(lock.read_bytes()).hexdigest(),
        "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
        "source_tree": subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip(),
    }
    if role is not None:
        report["factory"] = role
    if prepare_factory_lock:
        return prepare_lock(root, factory_root, factory_commit, role, environment, report)
    if component == "scanner-host-tools":
        invocation, report["scanner_tools"] = nix_scanner_tools.command(
            NIX, scanner_source or "git+file://" + str(root), environment)
        if scanner_source is not None:
            report["application"] = {"source_commit": application_commit,
                "source_tree": subprocess.check_output(["git", "rev-parse", application_commit + "^{tree}"],
                                                       cwd=application_root, text=True).strip(),
                "lock_sha256": hashlib.sha256(factory_source.committed(
                    application_root, application_commit, "flake.lock")).hexdigest()}
    if component == "scanner-host-tools" and operation == "check":
        report["passed"] = True
        return retain_report(root, report, 0)
    result = subprocess.run(invocation, cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
    report["passed"] = result.returncode == 0
    if result.returncode == 0 and operation == "check" and role is None:
        result, report["nonflake"] = nonflake_identity(root, environment)
        report["passed"] = result.returncode == 0
    if result.returncode == 0 and operation == "build":
        report["builds"] = json.loads(result.stdout)
        outputs = [path for build in report["builds"] for path in build["outputs"].values()]
        if not outputs:
            raise ValueError("Nix reported a successful build with no output paths")
        closure = subprocess.run(NIX + ["path-info", "--json", "--recursive", *outputs],
                                 cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
        report["passed"] = closure.returncode == 0
        if closure.returncode == 0:
            report["closure"] = json.loads(closure.stdout)
        else:
            result = closure
        if report["passed"] and archive is not None:
            result = nix_archive.copy_closure(NIX, outputs, archive, environment)
            report["archive"] = {"path": str(archive), "publication": "nonrelease"}
            report["passed"] = result.returncode == 0
    return retain_report(root, report, result.returncode)


def nonflake_identity(root, environment):
    """Require default.nix to instantiate the flake's exact application for every system."""
    identities = {}
    for system in SYSTEMS.values():
        flake = subprocess.run(NIX + ["eval", "--raw", "--no-write-lock-file",
                                      f"git+file://{root}#packages.{system}.default.drvPath"],
                               cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
        if flake.returncode != 0:
            return flake, identities
        entry = subprocess.run(["nix-instantiate", "--arg", "stdenv", f'{{ hostPlatform.system = "{system}"; }}',
                                str(root / "default.nix")],
                               cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
        identities[system] = {"flake": flake.stdout.strip(), "nonflake": entry.stdout.strip()}
        if entry.returncode != 0:
            return entry, identities
        if identities[system]["flake"] != identities[system]["nonflake"]:
            print(f"error: default.nix selects a different {system} derivation than flake.nix", file=sys.stderr)
            return subprocess.CompletedProcess(entry.args, 1), identities
    return subprocess.CompletedProcess([], 0), identities


def retain_report(root, report, status):
    directory = artifact_root(root) / "nix"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{report['operation']}-{time.time_ns()}.json"
    path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(f"Nix {report['operation']} report: {path}")
    return status


def prepare_lock(root, factory_root, commit, role, environment, report):
    directory = artifact_root(root) / "nix" / ("factory-lock-" + str(time.time_ns()))
    directory.mkdir(parents=True)
    with tempfile.TemporaryDirectory(prefix="factory-lock-") as temporary:
        candidate = Path(temporary)
        (candidate / "flake.nix").write_bytes(factory_source.committed(
            factory_root, commit, role["subdirectory"] + "/flake.nix"))
        result = subprocess.run(NIX + ["flake", "lock", str(candidate)], env=environment)
        report["operation"] = "prepare-factory-lock"
        report["passed"] = result.returncode == 0
        if result.returncode == 0:
            raw = (candidate / "flake.lock").read_bytes()
            report["factory"].update(factory_source.validate_lock(factory_root, role, raw))
            (directory / "flake.lock").write_bytes(raw)
    (directory / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(f"Candidate factory lock and provenance: {directory}")
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("check", "build"))
    parser.add_argument("--component", choices=COMPONENTS, default="default")
    parser.add_argument("--archive-dir", type=Path)
    parser.add_argument("--factory-source-root", type=Path)
    parser.add_argument("--factory-source-commit")
    parser.add_argument("--factory-channel", choices=factory_source.CHANNELS)
    parser.add_argument("--prepare-factory-lock", action="store_true")
    parser.add_argument("--application-source-root", type=Path)
    parser.add_argument("--application-source-commit")
    parser.add_argument("--prepare", action="store_true",
                        help="provision pinned Nix and every locked input, then evaluate nothing")
    args = parser.parse_args()
    try:
        if args.prepare:
            if args.operation != "check":
                raise ValueError("--prepare provisions evaluation inputs for check")
            return nix_runner.prepare(NIX, ROOT, credential_free_environment())
        return execute(args.operation, component=args.component, archive_dir=args.archive_dir,
                       factory_root=args.factory_source_root, factory_commit=args.factory_source_commit,
                       factory_channel=args.factory_channel, prepare_factory_lock=args.prepare_factory_lock,
                       application_root=args.application_source_root, application_commit=args.application_source_commit)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
