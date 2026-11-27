"""Standalone generators, independent D3 references, shrinker, and D4 checker
for the catalog, change-feed, and search bug-bash campaign (CF02).

This package never imports product code (D3, D6).
"""

from .seeds import (
    BASE_SEED_PRIMARY,
    BASE_SEED_SECONDARY,
    ReplaySelector,
    derive_seed,
    seed_rng,
)

__all__ = [
    "BASE_SEED_PRIMARY",
    "BASE_SEED_SECONDARY",
    "ReplaySelector",
    "derive_seed",
    "seed_rng",
]
