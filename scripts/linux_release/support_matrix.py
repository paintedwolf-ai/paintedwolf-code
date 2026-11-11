"""Expand the Linux support contract and qualification variants into stable rows."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys

CATALOG = Path(__file__).resolve().parents[2] / "packaging/linux/support-matrix.json"
IDENTIFIER = re.compile(r"[a-z][a-z0-9-]*\Z")
TIERS = {"installed-functional", "native-desktop", "native-security", "native-performance"}


def fields(value: object, names: set[str]) -> dict:
    if not isinstance(value, dict) or value.keys() != names:
        raise ValueError("qualification metadata has missing or unexpected fields")
    return value


def identifiers(value: object) -> list[str]:
    if (not isinstance(value, list) or not value or any(not isinstance(item, str) or not IDENTIFIER.fullmatch(item) for item in value)
            or len(set(value)) != len(value)):
        raise ValueError("qualification metadata requires distinct identifiers")
    return value


def qualification(environment: dict, capabilities: set[str]) -> dict:
    document = fields(environment.get("qualification"), {"releases", "display", "scaling"})
    releases = document["releases"]
    if not isinstance(releases, list) or not releases:
        raise ValueError("qualification requires explicit release variants")
    seen = set()
    for raw in releases:
        release = fields(raw, {"id", "os_ids", "version_id", "snapshot_kind", "channel"})
        identifier = identifiers([release["id"]])[0]
        if identifier in seen:
            raise ValueError("qualification repeats a release variant")
        seen.add(identifier)
        identifiers(release["os_ids"])
        version, channel = release["version_id"], release["channel"]
        if version is not None and (not isinstance(version, str) or not re.fullmatch(r"[0-9]+(?:\.[0-9]+)*", version)):
            raise ValueError("qualification release version must be exact or explicitly snapshot-pinned")
        if release["snapshot_kind"] not in {"package-inventory", "nixos-revision"}:
            raise ValueError("qualification release has an unknown snapshot authority")
        if release["snapshot_kind"] == "nixos-revision":
            if release["os_ids"] != ["nixos"] or channel not in {"nixos-26.05", "nixos-unstable"}:
                raise ValueError("NixOS qualification requires an explicit supported channel")
        elif channel is not None:
            raise ValueError("native package inventory cannot assert a NixOS channel")
    display = fields(document["display"], {"protocol", "xwayland", "clipboard_manager"})
    if (display["protocol"] not in {"x11", "wayland"} or display["xwayland"] not in {"disabled", "permitted"}
            or display["clipboard_manager"] not in {"absent", "permitted"}
            or display["protocol"] == "x11" and display["xwayland"] == "disabled"):
        raise ValueError("qualification display policy is invalid")
    scaling = fields(document["scaling"], {"uniform_percent", "uniform_capabilities", "mixed_percent", "mixed_capabilities"})
    for name in ("uniform_percent", "mixed_percent"):
        values = scaling[name]
        if (not isinstance(values, list) or not values or any(type(item) is not int or item < 100 or item > 400 for item in values)
                or len(set(values)) != len(values)):
            raise ValueError("qualification requires explicit distinct scale percentages")
    if len(scaling["mixed_percent"]) < 2 or not set(scaling["mixed_percent"]) <= set(scaling["uniform_percent"]):
        raise ValueError("mixed monitor scales must name at least two declared uniform scales")
    uniform = scaling["uniform_capabilities"]
    if (not isinstance(uniform, list) or any(item not in capabilities for item in uniform)
            or len(set(uniform)) != len(uniform) or 100 not in scaling["uniform_percent"]):
        raise ValueError("uniform scaling references unknown capabilities or omits the baseline scale")
    mixed = scaling["mixed_capabilities"]
    if not isinstance(mixed, list) or any(item not in uniform for item in mixed) or len(set(mixed)) != len(mixed):
        raise ValueError("mixed scaling references unknown or duplicate capabilities")
    return document


def variants(environment: dict, capability: str, capabilities: set[str]) -> list[dict]:
    document = qualification(environment, capabilities)
    if capability not in capabilities:
        raise ValueError("qualification variant requested an unknown capability")
    scaling = document["scaling"]
    required = scaling["uniform_percent"] if capability in scaling["uniform_capabilities"] else [100]
    scales = [("scale-" + str(value), [value]) for value in required]
    if capability in document["scaling"]["mixed_capabilities"]:
        scales.append(("mixed", document["scaling"]["mixed_percent"]))
    return [{"id": release["id"] + "-" + name, "release": release, "display": document["display"], "scales_percent": values}
            for release in document["releases"] for name, values in scales]


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


def text_fields(item: dict, names: tuple[str, ...]) -> None:
    for field in names:
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
        qualification(environment, set(capabilities))
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
