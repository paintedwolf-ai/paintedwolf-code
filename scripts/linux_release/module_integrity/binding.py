"""Bind a canonical inventory before the candidate generation's final seal."""
from __future__ import annotations

import hashlib
from pathlib import Path

from linux_release.module_integrity.abi import ELF, describe
from linux_release.module_integrity.inventory import CAPACITY, decode

INVENTORY_MARKER = b"PW-INV-SLOT-V1\0\0"
DIGEST_MARKER = b"PW-HASH-SLOT-V1\0"
GUARD_MARKER = b"PW-GUARD-HASH-V1"


def slot(image: ELF, name: str, size: int, marker: bytes) -> tuple[int, int]:
    matches = [row for row in image.symbols() if row[0] == name]
    if len(matches) != 1:
        raise ValueError("module binding symbol must be unique")
    _, info, other, section, address, length = matches[0]
    segments = image.contains(1, address, size)
    if info != 0x11 or other != 0 or section == 0 or length != size or len(segments) != 1:
        raise ValueError("invalid module binding symbol")
    segment = segments[0]
    if segment[1] & 2 or address + size > segment[3] + segment[5]:
        raise ValueError("module binding must be read-only file-backed bytes")
    offset = segment[2] + address - segment[3]
    data = image.bytes(offset, size)
    if data[:len(marker)] != marker or any(data[len(marker):]):
        raise ValueError("module binding slot is not an unbound producer input")
    return offset, size


def bind(auditor: Path, guard: Path, inventory: bytes, output: Path) -> dict:
    """Write new candidate anchors; never modify the source or publish a seal.

    Source custody, runtime capture and candidate publication belong to the
    generation transaction. These byte transformations authorize no paths.
    """
    selected = decode(inventory)
    original = describe(auditor, guard)
    if (original["architecture"] != selected["architecture"]
            or original["guard_witness_offset"] != selected["guard_witness_offset"]):
        raise ValueError("inventory differs from final guard ABI")
    audit, gate = ELF(auditor), ELF(guard)
    audit_slot, audit_size = slot(audit, "pw_module_inventory_v1", CAPACITY, INVENTORY_MARKER)
    guard_hash_slot, guard_hash_size = slot(audit, "pw_module_guard_digest_v1", 48, GUARD_MARKER)
    gate_slot, gate_size = slot(gate, "pw_module_inventory_digest_v1", 48, DIGEST_MARKER)
    gate_address = next(row[4] for row in gate.symbols() if row[0] == "pw_module_inventory_digest_v1")
    if gate_address != selected["guard_inventory_offset"]:
        raise ValueError("inventory differs from final guard digest ABI")
    bound_audit, bound_guard = bytearray(audit.raw), bytearray(gate.raw)
    value = INVENTORY_MARKER + inventory
    bound_audit[audit_slot:audit_slot + audit_size] = value + bytes(audit_size - len(value))
    bound_guard[gate_slot:gate_slot + gate_size] = DIGEST_MARKER + hashlib.sha256(inventory).digest()
    bound_audit[guard_hash_slot:guard_hash_slot + guard_hash_size] = GUARD_MARKER + hashlib.sha256(bound_guard).digest()
    if output.is_symlink() or not output.is_dir():
        raise ValueError("module candidate directory must be physical")
    # Exclusive outputs are preparation, not an atomic generation publication.
    paths = {"auditor": output / "pw-module-audit.so", "guard": output / "pw-module-guard.so",
             "inventory": output / "module-inventory.bin"}
    if any(path.exists() or path.is_symlink() for path in paths.values()):
        raise ValueError("module candidate outputs already exist")
    for name, data in (("auditor", bound_audit), ("guard", bound_guard), ("inventory", inventory)):
        with paths[name].open("xb") as handle:
            handle.write(data)
    final = describe(paths["auditor"], paths["guard"])
    return {"format_version": 1, "inventory_sha256": selected["sha256"],
            "input_auditor_sha256": original["auditor_sha256"],
            "input_guard_sha256": original["guard_sha256"], "final_abi": final}
