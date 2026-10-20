"""Generate literal user-namespace attachments for selected package engines."""

import hashlib
import json
from pathlib import Path
import stat
import struct
import sys


def literal(value):
    if not value or any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise ValueError("AppArmor attachment contains a control character")
    return '"' + ''.join('\\' + char if char in '\\"*?[]{}^' else char
                         for char in value) + '"'


def profile(package):
    package = Path(package)
    if not package.is_absolute() or package.resolve() != package:
        raise ValueError("selected generation must be physical and absolute")
    root = package / "lib/painted-wolf-code"
    engine = root / "bin/pw"
    manifest = root / "manifest.json"
    for path in (root, engine.parent, engine, manifest):
        if path.is_symlink() or path.resolve() != path:
            raise ValueError("selected engine and manifest must be physical")
    if not stat.S_ISREG(engine.stat().st_mode) or not engine.stat().st_mode & 0o111:
        raise ValueError("selected engine must be an executable regular file")
    if not stat.S_ISREG(manifest.stat().st_mode):
        raise ValueError("manifest must be a regular file")
    if manifest.stat().st_size > 4 * 1024 * 1024:
        raise ValueError("manifest exceeds bound")
    facts = json.loads(manifest.read_text())
    if (facts.get("format_version") != 1 or facts.get("package") != "painted-wolf-code"
            or facts.get("install_root") != str(root)):
        raise ValueError("manifest does not identify the selected package")
    machine = {"x86_64": 62, "aarch64": 183}.get(facts.get("architecture"))
    with engine.open("rb") as source:
        header = source.read(20)
        source.seek(0)
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    if (len(header) != 20 or header[:7] != b"\x7fELF\x02\x01\x01"
            or machine is None or struct.unpack_from("<H", header, 18)[0] != machine):
        raise ValueError("selected engine must be the manifest's native ELF")
    entries = [entry for entry in facts.get("executables", [])
               if entry.get("path") == str(engine)]
    if (len(entries) != 1 or entries[0].get("sha256") != digest
            or entries[0].get("size") != engine.stat().st_size):
        raise ValueError("selected engine bytes differ from final manifest")
    name = "paintedwolf-engine-" + hashlib.sha256(str(engine).encode()).hexdigest()
    return (f"profile {name} {literal(str(engine))} flags=(unconfined) {{\n"
            "  userns,\n}\n")


if __name__ == "__main__":
    if not 1 <= len(sys.argv[1:]) <= 8 or len(set(sys.argv[1:])) != len(sys.argv[1:]):
        raise SystemExit("select one to eight distinct package generations")
    print("abi <abi/4.0>,\n" + "".join(profile(path) for path in sys.argv[1:]))
