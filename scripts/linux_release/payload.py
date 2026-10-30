"""Shared required helper roles for Linux package construction and integrity checks."""

import json
from pathlib import Path
import re

CATALOG = Path(__file__).resolve().parents[2] / "packaging/linux/payload.json"
NAME = re.compile(r"[a-z][a-z0-9-]*\Z")


def load(path=CATALOG):
    catalog = json.loads(path.read_text(encoding="utf-8"))
    if catalog.get("format_version") != 1:
        raise ValueError("unsupported Linux payload catalog version")
    for field in ("helpers", "entry_points"):
        values = catalog.get(field)
        if (not isinstance(values, list) or not values
                or any(not isinstance(value, str) or not NAME.fullmatch(value) for value in values)
                or len(set(values)) != len(values)):
            raise ValueError(f"payload.{field} must name distinct executable roles")
    if not set(catalog["entry_points"]).issubset(catalog["helpers"]):
        raise ValueError("Linux entry points must name required helpers")
    bootstrap = catalog.get("setuid_bootstrap")
    if (not isinstance(bootstrap, dict) or set(bootstrap) != {"name", "path"}
            or not isinstance(bootstrap["name"], str) or not NAME.fullmatch(bootstrap["name"])
            or bootstrap["name"] in catalog["helpers"]
            or bootstrap["path"] != "/usr/libexec/painted-wolf-code/" + bootstrap["name"]):
        raise ValueError("payload.setuid_bootstrap must name one program installed outside the payload under /usr/libexec/painted-wolf-code")
    return catalog
