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

from ..artifact_paths import artifact_root
from . import nix_archive

ROOT = Path(__file__).resolve().parents[2]
SYSTEMS = {"x86_64": "x86_64-linux", "aarch64": "aarch64-linux"}
NIX = ["nix", "--extra-experimental-features", "nix-command flakes"]
COMPONENTS = ("default", "bun-lock", "frontend", "shell", "engine", "bound-engine", "bialy", "document-core",
              "desktop-settings-observer", "desktop-shell-check", "resources", "browser", "gitengine", "ocr", "decision-resources", "scanner-original", "scanner-finalized", "notices", "notice-tools",
              "nixos-module", "nixos-gnome-vm", "nixos-plasma-vm", "nix-workflow-fixture")


def command(operation: str, root: Path, component: str = "default") -> list[str]:
    source = "git+file://" + str(root)
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
            archive_dir: Path | None = None) -> int:
    lock = root / "flake.lock"
    if not lock.is_file():
        raise ValueError("flake.lock is required; evaluation/build must not create or update it")
    if archive_dir is not None and operation != "build":
        raise ValueError("closure archives require a build operation")
    archive = nix_archive.destination(root, archive_dir)
    invocation = command(operation, root, component)
    environment = dict(os.environ)
    for name in ("GH_TOKEN", "GITHUB_TOKEN", "LINUX_PACKAGE_SIGNING_KEY",
                 "FEED_SIGNING_KEYS_JSON", "TAURI_SIGNING_PRIVATE_KEY", "TAURI_SIGNING_PRIVATE_KEY_PASSWORD"):
        environment.pop(name, None)
    report = {
        "format_version": 1, "operation": operation, "component": component,
        "lock_sha256": hashlib.sha256(lock.read_bytes()).hexdigest(),
        "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
        "source_tree": subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip(),
    }
    result = subprocess.run(invocation, cwd=root, env=environment, text=True, stdout=subprocess.PIPE)
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
    directory = artifact_root(root) / "nix"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{operation}-{time.time_ns()}.json"
    path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(f"Nix {operation} report: {path}")
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("check", "build"))
    parser.add_argument("--component", choices=COMPONENTS, default="default")
    parser.add_argument("--archive-dir", type=Path)
    args = parser.parse_args()
    try:
        return execute(args.operation, component=args.component, archive_dir=args.archive_dir)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
