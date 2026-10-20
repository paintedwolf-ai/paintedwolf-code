"""Typed release, display and scaling requirements for installed qualification."""
from __future__ import annotations

import re

IDENTIFIER = re.compile(r"[a-z][a-z0-9-]*\Z")


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
    scaling = fields(document["scaling"], {"uniform_percent", "mixed_percent", "mixed_capabilities"})
    for name in ("uniform_percent", "mixed_percent"):
        values = scaling[name]
        if (not isinstance(values, list) or not values or any(type(item) is not int or item < 100 or item > 400 for item in values)
                or len(set(values)) != len(values)):
            raise ValueError("qualification requires explicit distinct scale percentages")
    if len(scaling["mixed_percent"]) < 2 or not set(scaling["mixed_percent"]) <= set(scaling["uniform_percent"]):
        raise ValueError("mixed monitor scales must name at least two declared uniform scales")
    mixed = scaling["mixed_capabilities"]
    if not isinstance(mixed, list) or any(item not in capabilities for item in mixed) or len(set(mixed)) != len(mixed):
        raise ValueError("mixed scaling references unknown or duplicate capabilities")
    return document


def variants(environment: dict, capability: str, capabilities: set[str]) -> list[dict]:
    document = qualification(environment, capabilities)
    if capability not in capabilities:
        raise ValueError("qualification variant requested an unknown capability")
    scales = [("scale-" + str(value), [value]) for value in document["scaling"]["uniform_percent"]]
    if capability in document["scaling"]["mixed_capabilities"]:
        scales.append(("mixed", document["scaling"]["mixed_percent"]))
    return [{"id": release["id"] + "-" + name, "release": release, "display": document["display"], "scales_percent": values}
            for release in document["releases"] for name, values in scales]
