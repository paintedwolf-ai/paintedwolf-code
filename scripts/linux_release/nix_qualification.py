"""Bind a successful hosted Nix build to its source, runner and consumers."""

from __future__ import annotations

import json
import os
from pathlib import Path
import platform
import re

from . import nix_scanner_host, nix_vm

COMMIT = re.compile(r"[0-9a-f]{40}\Z")
# Components whose retained closure is a qualification artifact.
ARCHIVED = ("default", "scanner-finalized", "desktop-settings-observer", "shell", "desktop-shell-check")


def hosted(environment) -> None:
    if environment.get("GITHUB_ACTIONS") != "true" or environment.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise ValueError("qualification binds builds only on an ephemeral GitHub-hosted runner")


def output(report) -> str:
    paths = [path for build in report.get("builds", []) for path in build.get("outputs", {}).values()]
    if len(paths) != 1:
        raise ValueError("qualification requires exactly one built output")
    return paths[0]


def source_manifest(report, *, architecture, controller_commit) -> tuple[str, dict]:
    component = report.get("component")
    if component not in ARCHIVED or report.get("archive", {}).get("publication") != "nonrelease":
        raise ValueError("qualification manifests describe a retained nonrelease closure")
    if not COMMIT.fullmatch(controller_commit or ""):
        raise ValueError("qualification requires the exact controller commit")
    manifest = {key: report[key] for key in ("source_commit", "source_tree", "lock_sha256")}
    manifest.update(architecture=architecture, controller_commit=controller_commit, execution="native",
                    publication="nonrelease", scope="Nix closure build; installed desktop qualification is separate")
    if component == "default":
        return "qualification-source.json", manifest
    manifest.update(component=component,
                    scope="Selected component only; no application closure or installed qualification")
    return "component-source.json", manifest


def qualify(report: dict, evidence: Path, environment=os.environ, authorize=nix_scanner_host.authorize_scanner) -> None:
    """Record what a passed build qualifies; unknown components qualify nothing."""
    hosted(environment)
    if report.get("passed") is not True or report.get("operation") != "build":
        raise ValueError("only a passed build can be qualified")
    component, built = report.get("component"), output(report)
    if component == "scanner-host-tools":
        selected = report["scanner_tools"]
        if built != selected["output"]:
            raise ValueError("realized scanner output differs from the admitted derivation")
        (evidence / "scanner-builder-permission.json").write_text(
            json.dumps(authorize(selected["executable"]), indent=2) + "\n")
    elif component in ARCHIVED:
        name, manifest = source_manifest(report, architecture=platform.machine(),
                                         controller_commit=environment.get("GITHUB_SHA"))
        (evidence / name).write_text(json.dumps(manifest, indent=2) + "\n")
    elif component in ("nixos-gnome-vm", "nixos-plasma-vm"):
        application = json.loads((evidence / "nix" / "application-producer.json").read_text())
        factory = report.get("factory") or {}
        manifest = nix_vm.bind(application, report, desktop=component.split("-")[1],
                               source_commit=application["source_commit"], source_tree=application["source_tree"],
                               lock_sha256=application["lock_sha256"], architecture=platform.machine(),
                               controller_commit=environment.get("GITHUB_SHA"),
                               factory_source={key: factory.get(key) for key in ("source_commit", "source_tree", "channel")})
        archive = Path(report["archive"]["path"])
        with (archive / "paintedwolf-vm-qualification.json").open("x") as stream:
            stream.write(json.dumps(manifest, indent=2) + "\n")
        (evidence / (component.removesuffix("-vm") + "-qualification.json")).write_text(json.dumps(manifest, indent=2) + "\n")
    else:
        raise ValueError(f"{component} has no hosted qualification binding")
