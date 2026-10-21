"""Bind nonrelease NixOS VM factories to the exact qualified application closure."""
from __future__ import annotations

import re

from .nix_closure import describe


def output(report):
    paths = [path for build in report.get("builds", []) for path in build.get("outputs", {}).values()]
    if len(paths) != 1 or paths[0] not in report.get("closure", {}):
        raise ValueError("qualification requires one built output in its complete closure")
    return paths[0]


def bind(application, factory, *, desktop, source_commit, source_tree, lock_sha256, architecture, controller_commit, factory_source=None):
    if desktop not in {"gnome", "plasma"} or architecture not in {"x86_64", "aarch64"}:
        raise ValueError("VM qualification requires a declared desktop and native architecture")
    expected = {"source_commit": source_commit, "source_tree": source_tree, "lock_sha256": lock_sha256}
    if any(not isinstance(value, str) or not re.fullmatch("[0-9a-f]{" + str(size) + "}", value)
           for value, size in [(source_commit, 40), (source_tree, 40), (lock_sha256, 64), (controller_commit, 40)]):
        raise ValueError("qualification source and controller identities must be exact digests")
    for report, component in [(application, "default"), (factory, "nixos-" + desktop + "-vm")]:
        if (report.get("passed") is not True or report.get("component") != component
                or (component == "default" and any(report.get(key) != value for key, value in expected.items()))
                or (component != "default" and factory_source is None and any(report.get(key) != value for key, value in expected.items()))
                or report.get("archive", {}).get("publication") != "nonrelease"):
            raise ValueError("VM factory and application require matching successful nonrelease build reports")
    if factory_source is not None:
        role = factory.get("factory")
        if (not isinstance(role, dict) or any(role.get(key) != value for key, value in factory_source.items())
                or role.get("application_commit") != source_commit
                or role.get("channel") not in {"nixos-26.05", "nixos-unstable"}
                or any(not isinstance(role.get(key), str) or not re.fullmatch("[0-9a-f]{" + str(size) + "}", role[key])
                       for key, size in [("source_commit", 40), ("source_tree", 40), ("nixpkgs_revision", 40), ("lock_sha256", 64)])):
            raise ValueError("VM host factory input differs from independently selected channel/source pins")
    app_path, vm_path = output(application), output(factory)
    describe(application["closure"], [app_path], source_commit, lock_sha256)
    describe(factory["closure"], [vm_path], source_commit, lock_sha256)
    for path, row in application["closure"].items():
        observed = factory["closure"].get(path)
        if (observed is None or any(observed.get(key) != row.get(key) for key in ("narHash", "narSize"))
                or sorted(observed.get("references", [])) != sorted(row["references"])):
            raise ValueError("VM factory does not retain the exact complete application closure")
    return {"format_version": 1, **expected, "desktop": desktop, "architecture": architecture,
            "controller_commit": controller_commit, "execution": "native", "publication": "nonrelease",
            "application": {"store_path": app_path, "nar_hash": application["closure"][app_path]["narHash"]},
            "factory": {"store_path": vm_path, "nar_hash": factory["closure"][vm_path]["narHash"],
                        **({"source": factory["factory"]} if factory_source is not None else {})},
            "installed_qualified": False, "scope": "VM factory build; boot and installed interactions require execution"}
