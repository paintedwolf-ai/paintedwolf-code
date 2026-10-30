"""The protected launch's per-family system access, sealed into the payload.

`packaging/linux/protected-launch.json` names, for each package family, the
programs outside the generation that the protected namespace may execute
(WebKitGTK's helper processes, at the path that family's WebKitGTK package
installs them) and the library roots whose native code it may map: the
directories the system's WebKitGTK and GTK stack loads from. The installer
resolves `{multiarch}` for the running CPU and keeps what the system has
(`pw-generations install`).
"""

import json
from pathlib import Path, PurePosixPath
import re

CATALOG = Path(__file__).resolve().parents[2] / "packaging/linux/protected-launch.json"
PAYLOAD_NAME = "protected-launch.json"
FAMILIES = {"deb", "rpm", "pacman"}
ARCHITECTURES = {"amd64", "arm64"}
MULTIARCH = re.compile(r"[a-z0-9_]+-linux-gnu\Z")


def load(path=CATALOG):
    catalog = json.loads(Path(path).read_text(encoding="utf-8"))
    if not isinstance(catalog, dict) or set(catalog) != {"format_version", "architectures", "families"} or catalog["format_version"] != 1:
        raise ValueError("unsupported protected launch catalog")
    architectures = catalog["architectures"]
    if not isinstance(architectures, dict) or set(architectures) != ARCHITECTURES:
        raise ValueError("protected launch catalog must name every shipped architecture")
    for arch, row in architectures.items():
        if not isinstance(row, dict) or set(row) != {"multiarch"} or not isinstance(row["multiarch"], str) or not MULTIARCH.fullmatch(row["multiarch"]):
            raise ValueError(f"protected launch catalog has an invalid {arch} multiarch tuple")
    families = catalog["families"]
    if not isinstance(families, dict) or set(families) != FAMILIES:
        raise ValueError("protected launch catalog must name every package family")
    for family, row in families.items():
        if not isinstance(row, dict) or set(row) != {"system_executables", "library_roots"}:
            raise ValueError(f"protected launch catalog has an invalid {family} row")
        for field in ("system_executables", "library_roots"):
            values = row[field]
            if not isinstance(values, list) or not values or len(set(values)) != len(values):
                raise ValueError(f"protected launch catalog lists no {family} {field}")
        for template in (*row["system_executables"], *row["library_roots"]):
            resolved = template.replace("{multiarch}", "x86_64-linux-gnu") if isinstance(template, str) else ""
            path = PurePosixPath(resolved)
            if (not path.is_absolute() or str(path) != resolved or ".." in path.parts or "{" in resolved or "}" in resolved
                    or path.parts[:2] not in {("/", "usr")} or path.name == ""):
                raise ValueError(f"protected launch catalog has an invalid {family} program {template!r}")
    return catalog
