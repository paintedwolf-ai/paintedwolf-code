"""Deterministic seed derivation and replay selectors (D5, ledger.md)."""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import random

BASE_SEED_PRIMARY = "20261127"
BASE_SEED_SECONDARY = "3bb1464d"


def derive_seed(base_seed: str, family_id: str, instance_index: int) -> int:
    """Derive a deterministic 64-bit integer seed from (base_seed, family_id, instance_index)."""
    if not base_seed or not family_id:
        raise ValueError("base_seed and family_id must be non-empty")
    if instance_index < 0:
        raise ValueError("instance_index must be non-negative")
    payload = f"{base_seed}:{family_id}:{instance_index}".encode("utf-8")
    digest = hashlib.sha256(payload).digest()
    return int.from_bytes(digest[:8], "big")


def seed_rng(base_seed: str, family_id: str, instance_index: int) -> random.Random:
    """Return an isolated deterministic PRNG for the given instance."""
    return random.Random(derive_seed(base_seed, family_id, instance_index))


@dataclass(frozen=True)
class ReplaySelector:
    """Exact replay coordinates for a generated campaign instance."""

    base_seed: str
    family_id: str
    instance_index: int

    @property
    def derived_seed(self) -> int:
        return derive_seed(self.base_seed, self.family_id, self.instance_index)

    def cli_args(self) -> list[str]:
        return [
            "--seed",
            self.base_seed,
            "--family",
            self.family_id,
            "--instance",
            str(self.instance_index),
        ]

    def to_dict(self) -> dict[str, object]:
        return {
            "base_seed": self.base_seed,
            "family_id": self.family_id,
            "instance_index": self.instance_index,
            "derived_seed": self.derived_seed,
            "replay_args": self.cli_args(),
        }

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "ReplaySelector":
        return cls(
            base_seed=str(data["base_seed"]),
            family_id=str(data["family_id"]),
            instance_index=int(data["instance_index"]),
        )
