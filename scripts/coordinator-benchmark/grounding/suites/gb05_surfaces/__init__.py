"""GB05 · Speaking surfaces fixture preparation package (S01..S07, D5 tripled)."""
from .catalog import (
    ALL_GB05_SLOTS,
    GB05_BASE_COMMIT,
    GB05_FAMILY_IDS,
    S01_SLOTS,
    S02_SLOTS,
    S03_SLOTS,
    S04_SLOTS,
    S05_SLOTS,
    S06_SLOTS,
    S07_SLOTS,
    SLOTS_BY_SELECTOR,
    GB05SlotSpec,
    validate_gb05_catalog,
)
from .fixtures import (
    GB05SlotFixture,
    build_all_gb05_fixtures,
    build_gb05_fixture,
    render_wording,
)

__all__ = [
    "ALL_GB05_SLOTS",
    "GB05SlotFixture",
    "GB05SlotSpec",
    "GB05_BASE_COMMIT",
    "GB05_FAMILY_IDS",
    "S01_SLOTS",
    "S02_SLOTS",
    "S03_SLOTS",
    "S04_SLOTS",
    "S05_SLOTS",
    "S06_SLOTS",
    "S07_SLOTS",
    "SLOTS_BY_SELECTOR",
    "build_all_gb05_fixtures",
    "build_gb05_fixture",
    "render_wording",
    "validate_gb05_catalog",
]
