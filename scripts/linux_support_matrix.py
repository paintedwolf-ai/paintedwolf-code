"""Expand the Linux support contract into stable installed-qualification rows."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys

CATALOG = Path(__file__).resolve().parent.parent / "packaging/linux/support-matrix.json"
IDENTIFIER = re.compile(r"[a-z][a-z0-9-]*\Z")
TIERS = {"installed-functional", "native-desktop", "native-security", "native-performance"}


def indexed(value: object, name: str) -> dict[str, dict]:
    if not isinstance(value, list) or not value:
        raise ValueError(f"{name} must be a nonempty list")
    result = {}
    for item in value:
        if not isinstance(item, dict):
            raise ValueError(f"{name} entries must be objects")
        key = item.get("id")
        if not isinstance(key, str) or not IDENTIFIER.fullmatch(key) or key in result:
            raise ValueError(f"{name} has an invalid or duplicate id: {key!r}")
        result[key] = item
    return result


def references(item: dict, field: str, available: dict) -> list[str]:
    values = item.get(field)
    if (not isinstance(values, list) or not values
            or any(not isinstance(value, str) for value in values)
            or len(set(values)) != len(values) or any(value not in available for value in values)):
        raise ValueError(f"{item['id']}.{field} must name distinct declared entries")
    return values


def issues(item: dict) -> list[int]:
    values = item.get("issues")
    if (not isinstance(values, list) or not values
            or any(type(value) is not int or value <= 0 for value in values)
            or len(set(values)) != len(values)):
        raise ValueError(f"{item['id']}.issues must name distinct positive issue numbers")
    return values


def text_fields(item: dict, fields: tuple[str, ...]) -> None:
    for field in fields:
        if not isinstance(item.get(field), str) or not item[field].strip():
            raise ValueError(f"{item['id']}.{field} must be nonempty text")


def expand(catalog: dict) -> list[dict]:
    if catalog.get("format_version") != 1:
        raise ValueError("unsupported Linux support matrix format")
    architectures = indexed(catalog.get("architectures"), "architectures")
    installations = indexed(catalog.get("installations"), "installations")
    capabilities = indexed(catalog.get("capabilities"), "capabilities")
    environments = indexed(catalog.get("environments"), "environments")
    for architecture in architectures.values():
        text_fields(architecture, ("nix_system",))
    for installation in installations.values():
        text_fields(installation, ("package_owner", "vault_mode"))
        issues(installation)
    for capability in capabilities.values():
        text_fields(capability, ("outcome",))
        issues(capability)
        if capability.get("tier") not in TIERS:
            raise ValueError(f"{capability['id']} has an unknown evidence tier")
        cases = capability.get("cases")
        if (not isinstance(cases, list) or not cases
                or any(not isinstance(case, str) or not IDENTIFIER.fullmatch(case) for case in cases)
                or len(set(cases)) != len(cases)):
            raise ValueError(f"{capability['id']}.cases must name distinct scenarios")
    rows = []
    used_architectures, used_installations = set(), set()
    for environment in environments.values():
        text_fields(environment, ("distribution", "release", "desktop", "display", "session",
                                  "portal", "security", "scaling", "coverage"))
        issues(environment)
        arches = references(environment, "architectures", architectures)
        modes = references(environment, "installations", installations)
        used_architectures.update(arches)
        used_installations.update(modes)
        for arch in arches:
            for mode in modes:
                installation = installations[mode]
                for capability in capabilities.values():
                    rows.append({
                        "id": "/".join((environment["id"], mode, arch, capability["id"])),
                        "environment": environment["id"], "installation": mode,
                        "architecture": arch, "capability": capability["id"],
                        "tier": capability["tier"], "cases": capability["cases"],
                        "emulation_allowed": capability["tier"] == "installed-functional",
                        "issues": sorted(set(issues(environment) + issues(installation)
                                             + issues(capability))),
                    })
    if used_architectures != architectures.keys() or used_installations != installations.keys():
        raise ValueError("every declared architecture and installation must have an environment")
    return sorted(rows, key=lambda row: row["id"])


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    args = parser.parse_args()
    try:
        rows = expand(json.loads(args.catalog.read_text(encoding="utf-8")))
    except (OSError, ValueError, TypeError, AttributeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    json.dump({"format_version": 1, "rows": rows}, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
