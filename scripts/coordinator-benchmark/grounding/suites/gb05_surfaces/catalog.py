"""Explicit selector-to-assertion manifest for GB05 (Speaking surfaces: S01..S07).

Matches `ledger.md`, `grounding.selectors`, Decision 002 (COV-03/04/05),
Decision 008/014 (S06 = 13 sidecar + 2 in-process), and D5 tripling across
all 3 wording variants (`confident`, `hedged`, `completion_asserting`):
- S01: 5 sidecar slots (when citations are required)
- S02: 4 sidecar slots (host attachment, empty ledger attaches nothing)
- S03: 9 sidecar slots (citation repair, invalid repair returns CITATION_REPAIR_INVALID,
       COV-03 pinned draft retirement triggers & budget retention)
- S04: 8 sidecar slots (report documents, COV-04 sole fence unwrap,
       COORDINATOR_CLOSEOUT_NO_ANSWER, strict trailing report JSON, exhaustion)
- S05: 6 sidecar slots (presentation repair & exhaustion)
- S06: 13 sidecar + 2 in-process slots (mid-turn notes, COV-05 source_change#
       brief-only semantics, Decision 008/014 historical unknown intent boundary,
       crash between carrier message and note)
- S07: 4 sidecar slots (what a closeout may cite)

Total: 49 sidecar + 2 in-process = 51 base slots (153 per platform after D5 tripling).
"""
from __future__ import annotations

from dataclasses import dataclass

from grounding.selectors import FIXED_FAMILIES, WORDING_VARIANTS


GB05_FAMILY_IDS: tuple[str, ...] = ("S01", "S02", "S03", "S04", "S05", "S06", "S07")
GB05_BASE_COMMIT: str = "40c593ac1cc85bb31bb1a4f21812f20ab347e407"


@dataclass(frozen=True)
class GB05SlotSpec:
    selector: str
    family_id: str
    lane: str  # "sidecar" | "in_process"
    title: str
    assertion: str
    expected_code: str = ""
    wordings: tuple[str, ...] = WORDING_VARIANTS
    contract_refs: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        if self.family_id not in GB05_FAMILY_IDS:
            raise ValueError(f"Unexpected GB05 family_id {self.family_id!r}")
        if self.lane not in {"sidecar", "in_process"}:
            raise ValueError(f"Unsupported lane {self.lane!r}")


def _slot(
    selector: str,
    family_id: str,
    title: str,
    assertion: str,
    *,
    lane: str = "sidecar",
    expected_code: str = "",
    contract_refs: tuple[str, ...] = (),
) -> GB05SlotSpec:
    return GB05SlotSpec(
        selector=selector,
        family_id=family_id,
        lane=lane,
        title=title,
        assertion=assertion,
        expected_code=expected_code,
        contract_refs=contract_refs,
    )


S01_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S01-01-exempt-no-citable-observations", "S01", "Exempt speech when no citable observations exist", "Closeout without citable observations marks evidence_optional=true and passes across all 3 wordings.", contract_refs=("S01", "D5")),
    _slot("S01-02-required-after-citable-read", "S01", "Citations required once citable observation exists", "Closeout with citable read#1 but no citation is refused across all 3 wordings.", contract_refs=("S01", "D5")),
    _slot("S01-03-supported-citation-settles", "S01", "Supported citation settles with zero friction", "Closeout citing [read#1:3] settles with traced=true, cited_evidence populated, zero repair.", contract_refs=("S01", "D5")),
    _slot("S01-04-source-change-creates-no-duty", "S01", "source_change# alone creates no citation duty", "Session with only source_change# requires no citation duty (COV-05).", contract_refs=("S01", "COV-05", "D5")),
    _slot("S01-05-unreadable-ledger-denies-exemption", "S01", "Unreadable ledger never grants exemption", "Unreadable evidence ledger never sets evidence_optional=true.", contract_refs=("S01", "V07", "D5")),
)

S02_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S02-01-host-attached-in-delivery-log", "S02", "Every host-attached reference is in delivery log", "Host-assembled fallback attaches only handles present in the independent delivery log.", contract_refs=("S02", "D5")),
    _slot("S02-02-empty-ledger-attaches-nothing", "S02", "Empty delivery ledger attaches nothing", "Host-assembled fallback with empty ledger attaches zero references.", contract_refs=("S02", "D5")),
    _slot("S02-03-host-attached-labeled-distinct", "S02", "Host-attached references labeled as host_assembled", "Host-assembled closeout sets host_assembled=true and marks host-added provenance.", contract_refs=("S02", "D5")),
    _slot("S02-04-host-attachment-never-invents-ranges", "S02", "Host attachment never invents undelivered ranges", "Host-attached references stay within delivered ranges of model-visible records.", contract_refs=("S02", "D5")),
)

S03_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S03-01-valid-citation-repair-replaces-offender", "S03", "Valid citation repair replaces offending span", "Replacing invalid citation with observed [read#1:3] settles cleanly.", contract_refs=("S03", "D5")),
    _slot("S03-02-invalid-repair-missing-handle", "S03", "Invalid repair referencing missing handle returns CITATION_REPAIR_INVALID", "Invalid repair returns CITATION_REPAIR_INVALID, leaves retained draft standing, and never repeats effect.", expected_code="CITATION_REPAIR_INVALID", contract_refs=("S03", "COV-03", "D5")),
    _slot("S03-03-invalid-repair-malformed-patch", "S03", "Malformed repair payload returns CITATION_REPAIR_INVALID", "Malformed repair payload returns CITATION_REPAIR_INVALID and preserves retained draft.", expected_code="CITATION_REPAIR_INVALID", contract_refs=("S03", "D5")),
    _slot("S03-04-invalid-repair-wrong-target-span", "S03", "Repair targeting wrong span returns CITATION_REPAIR_INVALID", "Repair targeting non-existent occurrence returns CITATION_REPAIR_INVALID and preserves draft.", expected_code="CITATION_REPAIR_INVALID", contract_refs=("S03", "D5")),
    _slot("S03-05-operational-batch-retires-pinned-draft", "S03", "Operational tool batch retires pinned draft while retaining budgets", "Issuing an operational tool batch retires pinned draft and retains retry/friction budgets (COV-03).", contract_refs=("S03", "COV-03", "D5")),
    _slot("S03-06-terminal-worker-card-retires-draft", "S03", "Terminal worker card publication retires pinned draft even on failure", "Terminal worker card publication retires pinned draft even when worker fails (COV-03).", contract_refs=("S03", "COV-03", "D5")),
    _slot("S03-07-typed-coordinator-wake-retires-draft", "S03", "Typed coordinator wake retires pinned draft", "Typed coordinator wake event retires pinned draft cleanly (COV-03).", contract_refs=("S03", "COV-03", "D5")),
    _slot("S03-08-denied-batch-admission-retires-draft", "S03", "Denied batch admission retires pinned draft", "Denied tool batch admission retires pinned draft cleanly (COV-03).", contract_refs=("S03", "COV-03", "D5")),
    _slot("S03-09-retry-exhaustion-host-assembles", "S03", "Citation repair exhaustion falls back to host_assembled", "Repeated ungrounded repairs exhaust budget and emit host_assembled closeout with zero fabricated handles.", contract_refs=("S03", "D5")),
)

S04_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S04-01-valid-report-document-accepted", "S04", "Valid report document with grounded citations accepted", "Well-formed report document with grounded citations is stored as accepted.", contract_refs=("S04", "D5")),
    _slot("S04-02-sole-markdown-fence-unwrapped", "S04", "Only sole markdown/md fence is unwrapped", "Sole ```markdown or ```md wrapper is unwrapped; non-sole or other language fences are not (COV-04).", contract_refs=("S04", "COV-04", "D5")),
    _slot("S04-03-bare-json-returns-no-answer", "S04", "Bare JSON closeout returns COORDINATOR_CLOSEOUT_NO_ANSWER", "Bare JSON closeout without prose returns COORDINATOR_CLOSEOUT_NO_ANSWER (COV-04).", expected_code="COORDINATOR_CLOSEOUT_NO_ANSWER", contract_refs=("S04", "COV-04", "D5")),
    _slot("S04-04-empty-closeout-returns-no-answer", "S04", "Empty closeout returns COORDINATOR_CLOSEOUT_NO_ANSWER", "Empty closeout returns COORDINATOR_CLOSEOUT_NO_ANSWER (COV-04).", expected_code="COORDINATOR_CLOSEOUT_NO_ANSWER", contract_refs=("S04", "COV-04", "D5")),
    _slot("S04-05-strict-trailing-report-json-fence", "S04", "Strict trailing report JSON fence enforced", "Malformed or non-trailing report JSON fence is rejected (COV-04).", contract_refs=("S04", "COV-04", "D5")),
    _slot("S04-06-repair-preserves-tool-availability", "S04", "Document repair preserves tool availability vs final prose-only turn", "Mid-repair turn keeps tools available while final prose-only turn restricts tools (COV-04).", contract_refs=("S04", "COV-04", "D5")),
    _slot("S04-07-ungrounded-report-triggers-doc-repair", "S04", "Ungrounded citation in report triggers document repair", "Ungrounded citation in report document triggers repair loop before acceptance.", contract_refs=("S04", "D5")),
    _slot("S04-08-exhausted-report-stored-with-defects", "S04", "Exhausted report document stored with defects never accepted", "Exhausted report document is persisted with its defects and never marked accepted.", contract_refs=("S04", "D5")),
)

S05_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S05-01-clean-presentation-passes", "S05", "Clean grounded presentation passes with zero repair", "Grounded prose without leaked control tokens passes presentation check.", contract_refs=("S05", "D5")),
    _slot("S05-02-presentation-repair-fixes-leaked-directive", "S05", "Presentation repair removes leaked control directive", "Presentation repair cleans leaked directive without charging shared friction.", contract_refs=("S05", "D10", "D5")),
    _slot("S05-03-presentation-repair-preserves-citations", "S05", "Presentation repair preserves grounded citations", "Repaired presentation retains valid grounded citations intact.", contract_refs=("S05", "D5")),
    _slot("S05-04-presentation-repair-does-not-charge-friction", "S05", "Presentation repair spends retry_count without charging friction_units", "Presentation repair increments retry_count without charging shared friction_units.", contract_refs=("S05", "D10", "D5")),
    _slot("S05-05-presentation-cleanup-no-markdown-structure", "S05", "Pathless handle cleanup creates no new Markdown structure", "Cleanup of pathless handle / control directive creates no synthetic Markdown structure (COV-02).", contract_refs=("S05", "COV-02", "D5")),
    _slot("S05-06-presentation-repair-exhaustion", "S05", "Exhausted presentation repair settles deterministically", "Exhausting presentation repair settles deterministically with typed outcome.", contract_refs=("S05", "D5")),
)

S06_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S06-01-midturn-supported-note-published", "S06", "Supported mid-turn note published while tools still run", "Supported mid-turn note publishes with typed code and tools still execute.", contract_refs=("S06", "D5")),
    _slot("S06-02-midturn-unresolved-handle-withheld-charges-friction", "S06", "Mid-turn note with unresolved handle withheld and charges friction", "Unresolved handle withholds note with typed MID_TURN_NOTE_* code, tools still run, charges friction.", contract_refs=("S06", "D10", "D5")),
    _slot("S06-03-midturn-unresolved-url-withheld-charges-friction", "S06", "Mid-turn note with unresolved URL withheld and charges friction", "Unresolved URL withholds note with typed MID_TURN_NOTE_* code, tools still run, charges friction.", contract_refs=("S06", "D10", "D5")),
    _slot("S06-04-midturn-brief-only-status-no-friction", "S06", "Brief status mid-turn note without unresolved refs charges zero friction", "Brief status note without unresolved handles/URLs charges zero friction.", contract_refs=("S06", "D10", "D5")),
    _slot("S06-05-midturn-source-change-proves-brief-row", "S06", "source_change# proves only brief row", "source_change# supports brief row only (COV-05).", contract_refs=("S06", "COV-05", "D5")),
    _slot("S06-06-midturn-source-change-no-line-or-excerpt", "S06", "source_change# supplies no line or excerpt proof", "source_change# never supports line or excerpt citations (COV-05).", contract_refs=("S06", "COV-05", "D5")),
    _slot("S06-07-midturn-source-change-not-recallable", "S06", "source_change# unavailable to recall", "source_change# is excluded from recall tool (COV-05).", contract_refs=("S06", "COV-05", "D5")),
    _slot("S06-08-midturn-derived-user-note-no-second-copy", "S06", "Derived user note creates no second model-visible copy", "Derived user note does not mint a duplicate model-visible record (COV-05).", contract_refs=("S06", "COV-05", "D5")),
    _slot("S06-09-midturn-withheld-note-absent-from-user-view", "S06", "Withheld mid-turn note absent from user-facing feed", "Withheld note carries typed code in audit trail and is absent from user-facing display.", contract_refs=("S06", "D5")),
    _slot("S06-10-midturn-tools-always-execute", "S06", "Tool batch executes regardless of mid-turn note outcome", "Both published and withheld mid-turn notes allow paired tool calls to execute.", contract_refs=("S06", "D5")),
    _slot("S06-11-midturn-non-handle-warning-no-friction", "S06", "Non-handle/non-URL mid-turn row charges zero friction", "Friction is charged strictly for unresolved handles and URLs, never other note rows.", contract_refs=("S06", "D10", "D5")),
    _slot("S06-12-midturn-multi-row-mixed-outcomes", "S06", "Multiple mid-turn rows report per-row outcome and code", "Each mid-turn row records its own outcome and typed code.", contract_refs=("S06", "D5")),
    _slot("S06-13-historical-unknown-intent-boundary", "S06", "Decision 008/014 historical unknown pre/post-batch intent boundary", "Explicit pre-batch vs post-batch intent-window boundary across all 3 wordings (Decision 008/014).", contract_refs=("S06", "COV-05", "D13", "D5")),
    _slot("S06-14-inprocess-crash-after-carrier-before-note", "S06", "In-process crash between carrier message and mid-turn note", "Simulated crash between carrier message commit and note commit leaves consistent state.", lane="in_process", contract_refs=("S06", "D5")),
    _slot("S06-15-inprocess-crash-after-note-before-ack", "S06", "In-process crash after note persist before ack", "Simulated crash after note persist never duplicates note or loses tool execution.", lane="in_process", contract_refs=("S06", "D5")),
)

S07_SLOTS: tuple[GB05SlotSpec, ...] = (
    _slot("S07-01-closeout-cites-current-intent-reads", "S07", "Closeout may cite current intent-window observations", "Closeout citing current-window read#1 settles cleanly.", contract_refs=("S07", "D5")),
    _slot("S07-02-closeout-cites-completed-worker-leg", "S07", "Closeout may cite leg-namespaced completed worker evidence", "Closeout citing [leg-a:read#1:2] from completed worker leg settles cleanly.", contract_refs=("S07", "V06", "D5")),
    _slot("S07-03-closeout-refuses-cross-chat-handle", "S07", "Closeout refuses handle from another chat session", "Closeout citing handle from a second chat is refused across all 3 wordings.", contract_refs=("S07", "V06", "D5")),
    _slot("S07-04-closeout-refuses-unobserved-working-tree", "S07", "Closeout refuses undelivered working-tree file", "Closeout citing an unread file in workspace is refused across all 3 wordings.", contract_refs=("S07", "D5")),
)

ALL_GB05_SLOTS: tuple[GB05SlotSpec, ...] = (
    S01_SLOTS + S02_SLOTS + S03_SLOTS + S04_SLOTS + S05_SLOTS + S06_SLOTS + S07_SLOTS
)
SLOTS_BY_SELECTOR: dict[str, GB05SlotSpec] = {s.selector: s for s in ALL_GB05_SLOTS}


def validate_gb05_catalog() -> dict[str, int]:
    if len(SLOTS_BY_SELECTOR) != len(ALL_GB05_SLOTS):
        raise ValueError("Duplicate GB05 selector detected")
    ledger = {f.family_id: f for f in FIXED_FAMILIES if f.card == "GB05"}
    total_sidecar = 0
    total_inproc = 0
    for fam_id, meta in ledger.items():
        fam_slots = [s for s in ALL_GB05_SLOTS if s.family_id == fam_id]
        sc = sum(1 for s in fam_slots if s.lane == "sidecar")
        ip = sum(1 for s in fam_slots if s.lane == "in_process")
        if sc != meta.sidecar_count or ip != meta.in_process_count:
            raise ValueError(
                f"{fam_id} mismatch: catalog=({sc}, {ip}) vs ledger=({meta.sidecar_count}, {meta.in_process_count})"
            )
        total_sidecar += sc
        total_inproc += ip
    base = total_sidecar + total_inproc
    return {
        "sidecar_slots": total_sidecar,
        "in_process_slots": total_inproc,
        "base_slots": base,
        "d5_tripled_per_platform": base * len(WORDING_VARIANTS),
    }
