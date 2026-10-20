"""Native package lifecycle interactions, retaining failures and restoration results."""
from __future__ import annotations

import os
from pathlib import Path

from ..installed_runtime import bind
from .package_payload import inspect
from .package_native import command, current_package, operations, owner_commands
from .products import require_owned_guest
from .records import CaseUnavailable, retain


def execute(binding: dict, capability: str, case: str, evidence_root: Path) -> dict:
    if (capability, case) != ("installation", "package-ownership-and-removal"):
        return retain(binding, capability, case, evidence_root, "unverified",
                      {"reason": "native interaction driver or selected lifecycle fixtures required"})
    require_owned_guest(binding)
    package = current_package(binding)
    family = binding["artifact"]["installation"]
    streams, observations = {}, {"installation": family, "restored": False}

    def run(label, arguments):
        code, output, errors = command(arguments)
        streams[label + "-stdout"], streams[label + "-stderr"] = output, errors
        observations[label] = {"arguments": arguments, "exit_code": code}
        return code, output.decode("utf-8", errors="strict").strip()

    query, header = owner_commands(family, package, binding["launcher"]["path"])
    code, owner = run("installed-owner", query)
    if code:
        raise CaseUnavailable("public launcher has no native package owner")
    if family == "deb":
        suffix = ": " + binding["launcher"]["path"]
        if not owner.endswith(suffix) or "\n" in owner:
            raise ValueError("public launcher has ambiguous native ownership")
        owner = owner[:-len(suffix)].split(":", 1)[0]
    code, selected_owner = run("selected-owner", header)
    if code or owner != "painted-wolf-code" or owner != selected_owner:
        raise ValueError("selected final package differs from installed launcher owner")
    remove, install = operations(family, package, owner)
    entries = inspect(package, family, binding["artifact"]["identity"]["sha256"])
    status = "failed"
    try:
        code, _ = run("remove", remove)
        remaining = [entry.name for entry in entries.values()
                     if entry.kind != "directory" and os.path.lexists(Path("/") / entry.name)]
        observations["remaining_payload_paths"] = remaining
        status = "passed" if code == 0 and not remaining else "failed"
    except (OSError, ValueError) as error:
        observations["transaction_error"] = str(error)
        status = "failed"
    finally:
        try:
            # Revalidate bytes immediately before restoring; never install a changed fixture.
            current_package(binding)
            code, _ = run("restore", install)
            if code:
                raise ValueError("native package restoration failed; guest requires recovery")
            final = {"format_version": 1, "source_commit": binding["source_commit"], "artifacts": [binding["artifact"]]}
            rebound = bind(final, binding["source_commit"], binding["artifact"]["id"], package.parent, Path(binding["install_root"]))
            binding.update(rebound)
            observations["restored"] = True
        except (OSError, ValueError) as error:
            observations["restoration_error"] = str(error)
            status = "failed"
    return retain(binding, capability, case, evidence_root, status, observations, streams)
