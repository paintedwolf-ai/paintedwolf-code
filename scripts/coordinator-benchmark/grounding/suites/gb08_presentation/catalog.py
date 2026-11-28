"""Explicit selector-to-assertion catalog for GB08 (Den presentation, feedback & diagnostics).

Separates headless/sidecar presentation, feedback, and diagnostic slots from native Den
desktop UI slots (released on both hosts by Decision 050):

Headless / sidecar lane (`lane="sidecar"`):
- P01: Presentation wire payload (`cited_evidence`, `openable`, `evidence_optional`,
       `host_assembled`, refused partial state, withheld mid-turn note absence) — 6 slots
- P02: Markdown & copy-text presentation rendering (pathless handle stripping, code-span
       preservation & control directive removal, link rewriting with no new structure,
       raw HTML & broken link neutralization) — 4 slots
- P03: Serialized refusal feedback readback audit (unknown handle, uncovered range,
       excerpt mismatch, wrong path) — 4 slots
- P04: Opt-in dev diagnostic parity & contradiction detection (silent on healthy,
       all 3 contradiction classes, enabled vs disabled outcome/count parity) — 3 slots

Native Den desktop UI lane (`lane="native_den"`):
- DEN: Five native Den presentation states matching `DEN_PRESENTATION_SCENARIOS`
       (grounded navigable link, exempt no evidence state, host-added labeled,
       refused typed state, withheld mid-turn note absent) — 5 slots

Total: 17 headless/sidecar slots + 5 native Den UI slots = 22 GB08 slots.
"""
from __future__ import annotations

from dataclasses import dataclass

from .diagnostic import DEN_PRESENTATION_SCENARIOS, GB08_BASE_COMMIT


GB08_HEADLESS_FAMILY_IDS: tuple[str, ...] = ("P01", "P02", "P03", "P04")
GB08_NATIVE_FAMILY_IDS: tuple[str, ...] = ("DEN",)
GB08_FAMILY_IDS: tuple[str, ...] = GB08_HEADLESS_FAMILY_IDS + GB08_NATIVE_FAMILY_IDS


@dataclass(frozen=True)
class GB08SlotSpec:
    """One GB08 test slot across either the headless sidecar lane or native Den UI lane."""

    selector: str
    family_id: str
    lane: str  # "sidecar" | "native_den"
    title: str
    assertion: str
    den_state: str = ""
    contract_refs: tuple[str, ...] = ()


def _sidecar_slot(
    selector: str,
    family_id: str,
    title: str,
    assertion: str,
    *,
    contract_refs: tuple[str, ...] = (),
) -> GB08SlotSpec:
    return GB08SlotSpec(
        selector=selector,
        family_id=family_id,
        lane="sidecar",
        title=title,
        assertion=assertion,
        contract_refs=contract_refs,
    )


P01_SLOTS: tuple[GB08SlotSpec, ...] = (
    _sidecar_slot(
        "P01-01-grounded-cited-evidence-openable",
        "P01",
        "Grounded closeout wire payload carries openable cited_evidence",
        "Grounded closeout citing [read#1:3] sets traced=true, evidence_optional=false, host_assembled=false, and cited_evidence with grounded verdict and openable=true.",
        contract_refs=("GB08", "D4", "D10"),
    ),
    _sidecar_slot(
        "P01-02-exempt-speech-evidence-optional",
        "P01",
        "Exempt speech closeout sets evidence_optional=true with empty cited_evidence",
        "Closeout in an intent window with no citable observation sets traced=true, evidence_optional=true, host_assembled=false, and empty cited_evidence.",
        contract_refs=("GB08", "S01", "D10"),
    ),
    _sidecar_slot(
        "P01-03-host-assembled-closeout-flag",
        "P01",
        "Host-assembled closeout sets host_assembled=true with admissible references",
        "Uncited closeout after file observation triggers host attachment with host_assembled=true and every attached cited_evidence handle present in the DeliveryLog.",
        contract_refs=("GB08", "S02", "D10"),
    ),
    _sidecar_slot(
        "P01-04-refused-closeout-partial-wire-state",
        "P01",
        "Refused closeout never marks invalid citation grounded",
        "Refused closeout citing unobserved [read#99:99] spends friction and never records read#99 as grounded in cited_evidence.",
        contract_refs=("GB08", "D4", "D10"),
    ),
    _sidecar_slot(
        "P01-05-openable-false-for-nonfile-target",
        "P01",
        "Non-regular-file command/directory citation never stamps openable=true",
        "Citing a command or directory observation does not stamp openable=true for non-regular-file paths.",
        contract_refs=("GB08", "IsOpenablePath"),
    ),
    _sidecar_slot(
        "P01-06-withheld-midturn-note-absent-from-feed",
        "P01",
        "Withheld mid-turn note is absent from user-facing transcript feed",
        "A mid-turn note citing unobserved [read#99:99] is withheld from user-facing transcript messages while its tool batch still executes.",
        contract_refs=("GB08", "S06", "D10"),
    ),
)

P02_SLOTS: tuple[GB08SlotSpec, ...] = (
    _sidecar_slot(
        "P02-01-pathless-handle-stripped-or-rewritten",
        "P02",
        "Pathless handle removed and file handle rewritten cleanly",
        "Presented closeout removes pathless handles like [command#1] and rewrites resolved file references cleanly without raw handle debris.",
        contract_refs=("GB08", "crosswalk:presentation"),
    ),
    _sidecar_slot(
        "P02-02-control-directives-removed-code-preserved",
        "P02",
        "Control directives removed while inline and fenced code stay literal",
        "Presented closeout preserves literal inline code and fenced code blocks byte-for-byte without leaking control directives.",
        contract_refs=("GB08", "crosswalk:presentation"),
    ),
    _sidecar_slot(
        "P02-03-resolved-ref-rewritten-no-new-structure",
        "P02",
        "Resolved reference rewrite introduces no new Markdown structure",
        "Rewriting a resolved citation in single-paragraph prose introduces no new headings, lists, or raw HTML anchors.",
        contract_refs=("GB08", "crosswalk:presentation"),
    ),
    _sidecar_slot(
        "P02-04-raw-html-and-broken-links-neutralized",
        "P02",
        "Raw HTML anchors and broken links are neutralized in presentation",
        "Raw HTML anchor tags and empty/nowhere Markdown links in assistant prose are escaped or flattened to plain labels.",
        contract_refs=("GB08", "citationmd:present"),
    ),
)

P03_SLOTS: tuple[GB08SlotSpec, ...] = (
    _sidecar_slot(
        "P03-01-feedback-unknown-handle-admissible-samples",
        "P03",
        "Unknown-handle refusal feedback offers only admissible DeliveryLog handles",
        "Refusal for [read#99:99] names read#99 as the offender and every offered sample handle exists in the DeliveryLog.",
        contract_refs=("GB08", "R05", "D14"),
    ),
    _sidecar_slot(
        "P03-02-feedback-uncovered-range-offender-named",
        "P03",
        "Uncovered-range refusal feedback names the failing citation accurately",
        "Refusal for uncovered line [read#1:99] on a 3-line file names the failing citation and offers only admissible handles.",
        contract_refs=("GB08", "R05", "D14"),
    ),
    _sidecar_slot(
        "P03-03-feedback-excerpt-mismatch-offender-named",
        "P03",
        "Excerpt-mismatch refusal feedback names the failing citation accurately",
        "Refusal for mismatched excerpt on [read#1:3] names the failing citation and offers only admissible handles.",
        contract_refs=("GB08", "R05", "D14"),
    ),
    _sidecar_slot(
        "P03-04-feedback-wrong-path-offender-named",
        "P03",
        "Wrong-path refusal feedback names the failing citation accurately",
        "Refusal for handle paired with wrong path [read#1 wrong.go:3] names the failing citation and offers only admissible handles.",
        contract_refs=("GB08", "R05", "D14"),
    ),
)

P04_SLOTS: tuple[GB08SlotSpec, ...] = (
    _sidecar_slot(
        "P04-01-diagnostic-silent-on-healthy-closeout",
        "P04",
        "Opt-in dev diagnostic raises zero contradictions on healthy closeout",
        "On a grounded D1 closeout, enabled and disabled dev diagnostic both emit 0 contradictions.",
        contract_refs=("GB08", "Decision-021"),
    ),
    _sidecar_slot(
        "P04-02-diagnostic-detects-all-three-contradictions",
        "P04",
        "Opt-in dev diagnostic detects all 3 contradiction classes when enabled",
        "Against a real D1 DeliveryLog, enabled diagnostic detects grounded_without_support, refusal_with_unique_support, and accepted_after_refusal (deduplicated/bounded), while disabled emits 0.",
        contract_refs=("GB08", "Decision-021"),
    ),
    _sidecar_slot(
        "P04-03-diagnostic-enabled-vs-disabled-parity",
        "P04",
        "Enabled and disabled diagnostic runs yield identical outcomes and D10 counts",
        "Running grounded and refused closeouts with diagnostic enabled vs disabled yields identical verdicts, identical D10 friction counts, and zero side-effects.",
        contract_refs=("GB08", "Decision-021", "D10"),
    ),
)

DEN_SLOTS: tuple[GB08SlotSpec, ...] = tuple(
    GB08SlotSpec(
        selector=s.selector,
        family_id="DEN",
        lane="native_den",
        title=f"Native Den presentation state: {s.state}",
        assertion=s.assertion,
        den_state=s.state,
        contract_refs=("GB08", "D15", "Decision-050"),
    )
    for s in DEN_PRESENTATION_SCENARIOS
)

HEADLESS_GB08_SLOTS: tuple[GB08SlotSpec, ...] = P01_SLOTS + P02_SLOTS + P03_SLOTS + P04_SLOTS
NATIVE_GB08_SLOTS: tuple[GB08SlotSpec, ...] = DEN_SLOTS
ALL_GB08_SLOTS: tuple[GB08SlotSpec, ...] = HEADLESS_GB08_SLOTS + NATIVE_GB08_SLOTS
SLOTS_BY_SELECTOR: dict[str, GB08SlotSpec] = {s.selector: s for s in ALL_GB08_SLOTS}


def validate_gb08_catalog() -> dict[str, int]:
    """Validate GB08 catalog uniqueness, lane separation, and 1-to-1 alignment with DEN_PRESENTATION_SCENARIOS."""
    if len(SLOTS_BY_SELECTOR) != len(ALL_GB08_SLOTS):
        raise ValueError("Duplicate GB08 selector detected")

    for spec in HEADLESS_GB08_SLOTS:
        if spec.lane != "sidecar":
            raise ValueError(f"Headless slot {spec.selector} has non-sidecar lane {spec.lane!r}")
        if spec.family_id not in GB08_HEADLESS_FAMILY_IDS:
            raise ValueError(f"Headless slot {spec.selector} has unknown headless family {spec.family_id!r}")

    for spec in NATIVE_GB08_SLOTS:
        if spec.lane != "native_den":
            raise ValueError(f"Native slot {spec.selector} has non-native_den lane {spec.lane!r}")
        if spec.family_id not in GB08_NATIVE_FAMILY_IDS:
            raise ValueError(f"Native slot {spec.selector} has unknown native family {spec.family_id!r}")

    den_scenario_selectors = [s.selector for s in DEN_PRESENTATION_SCENARIOS]
    den_catalog_selectors = [s.selector for s in NATIVE_GB08_SLOTS]
    if den_scenario_selectors != den_catalog_selectors:
        raise ValueError(
            f"DEN_SLOTS mismatch with DEN_PRESENTATION_SCENARIOS: "
            f"catalog={den_catalog_selectors} vs scenarios={den_scenario_selectors}"
        )

    return {
        "headless_slots": len(HEADLESS_GB08_SLOTS),
        "native_slots": len(NATIVE_GB08_SLOTS),
        "total_slots": len(ALL_GB08_SLOTS),
    }


__all__ = [
    "ALL_GB08_SLOTS",
    "DEN_SLOTS",
    "GB08_BASE_COMMIT",
    "GB08_FAMILY_IDS",
    "GB08_HEADLESS_FAMILY_IDS",
    "GB08_NATIVE_FAMILY_IDS",
    "GB08SlotSpec",
    "HEADLESS_GB08_SLOTS",
    "NATIVE_GB08_SLOTS",
    "P01_SLOTS",
    "P02_SLOTS",
    "P03_SLOTS",
    "P04_SLOTS",
    "SLOTS_BY_SELECTOR",
    "validate_gb08_catalog",
]
