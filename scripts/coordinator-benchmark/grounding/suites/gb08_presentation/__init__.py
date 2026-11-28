"""GB08 · Den presentation, feedback readback, and opt-in dev diagnostic package."""
from .catalog import (
    ALL_GB08_SLOTS,
    DEN_SLOTS,
    GB08_FAMILY_IDS,
    GB08_HEADLESS_FAMILY_IDS,
    GB08_NATIVE_FAMILY_IDS,
    GB08SlotSpec,
    HEADLESS_GB08_SLOTS,
    NATIVE_GB08_SLOTS,
    SLOTS_BY_SELECTOR,
    validate_gb08_catalog,
)
from .diagnostic import (
    DEN_PRESENTATION_SCENARIOS,
    GB08_BASE_COMMIT,
    DenPresentationScenario,
    DiagnosticContradiction,
    FeedbackAuditResult,
    audit_refusal_feedback,
    run_dev_diagnostic,
)

__all__ = [
    "ALL_GB08_SLOTS",
    "DEN_PRESENTATION_SCENARIOS",
    "DEN_SLOTS",
    "DenPresentationScenario",
    "DiagnosticContradiction",
    "FeedbackAuditResult",
    "GB08SlotSpec",
    "GB08_BASE_COMMIT",
    "GB08_FAMILY_IDS",
    "GB08_HEADLESS_FAMILY_IDS",
    "GB08_NATIVE_FAMILY_IDS",
    "HEADLESS_GB08_SLOTS",
    "NATIVE_GB08_SLOTS",
    "SLOTS_BY_SELECTOR",
    "audit_refusal_feedback",
    "run_dev_diagnostic",
    "validate_gb08_catalog",
]
