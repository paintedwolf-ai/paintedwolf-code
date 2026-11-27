"""GB08 · Den presentation, feedback readback, and opt-in dev diagnostic package."""
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
    "DEN_PRESENTATION_SCENARIOS",
    "DenPresentationScenario",
    "DiagnosticContradiction",
    "FeedbackAuditResult",
    "GB08_BASE_COMMIT",
    "audit_refusal_feedback",
    "run_dev_diagnostic",
]
