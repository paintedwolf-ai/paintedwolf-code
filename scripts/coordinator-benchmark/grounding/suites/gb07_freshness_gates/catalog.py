"""Explicit selector-to-assertion manifest for GB07 (Freshness, closeout gates, friction: G01..G05).

Matches `ledger.md`, `grounding.selectors`, Decision 002 (COV-07), D8 (in-process block mode),
D10 (friction counters), and D5 tripling across all 3 wordings:
- G01: 7 sidecar slots (verification freshness: unchanged, content edit, unrelated file, touch/no-op)
- G02: 4 sidecar slots (proof layers do not substitute for one another)
- G03: 8 sidecar slots (each open closeout gate blocks closeout with its typed code)
- G04: 5 sidecar + 6 in-process slots (friction within intent & across phases, breaker at
       3 total and 2 consecutive in shipped mode, and D8 in-process `block` mode returning
       COORDINATOR_GROUNDING_ESCALATED)
- G05: 4 sidecar slots (search evidence after compaction; rebuild projection changes no record)

Total: 28 sidecar + 6 in-process = 34 base slots (102 per platform after D5 tripling).
"""
from __future__ import annotations

from dataclasses import dataclass

from grounding.selectors import FIXED_FAMILIES, WORDING_VARIANTS


GB07_FAMILY_IDS: tuple[str, ...] = ("G01", "G02", "G03", "G04", "G05")
GB07_BASE_COMMIT: str = "40c593ac1cc85bb31bb1a4f21812f20ab347e407"


@dataclass(frozen=True)
class GB07SlotSpec:
    selector: str
    family_id: str
    lane: str  # "sidecar" | "in_process"
    title: str
    assertion: str
    expected_code: str = ""
    wordings: tuple[str, ...] = WORDING_VARIANTS
    contract_refs: tuple[str, ...] = ()


def _slot(
    selector: str,
    family_id: str,
    title: str,
    assertion: str,
    *,
    lane: str = "sidecar",
    expected_code: str = "",
    contract_refs: tuple[str, ...] = (),
) -> GB07SlotSpec:
    return GB07SlotSpec(
        selector=selector,
        family_id=family_id,
        lane=lane,
        title=title,
        assertion=assertion,
        expected_code=expected_code,
        contract_refs=contract_refs,
    )


G01_SLOTS: tuple[GB07SlotSpec, ...] = (
    _slot("G01-01-fresh-receipt-unchanged-content", "G01", "Verification receipt counts while content is unchanged", "Unmodified verified file keeps verification receipt fresh.", contract_refs=("G01", "D5")),
    _slot("G01-02-stale-after-content-edit", "G01", "Editing verified file invalidates receipt", "Modifying content of verified file invalidates prior receipt.", contract_refs=("G01", "D5")),
    _slot("G01-03-fresh-after-unrelated-file-edit", "G01", "Editing unrelated file preserves receipt freshness", "Editing an unrelated file keeps verified file's receipt fresh.", contract_refs=("G01", "D5")),
    _slot("G01-04-fresh-after-noop-touch", "G01", "No-op rewrite with identical bytes preserves freshness", "Rewriting identical bytes preserves content-hash freshness.", contract_refs=("G01", "D5")),
    _slot("G01-05-stale-after-revert-and-reedit", "G01", "Re-editing after verification requires new receipt", "Changing content invalidates prior receipt until re-verified.", contract_refs=("G01", "D5")),
    _slot("G01-06-stale-after-delete", "G01", "Deleting verified file invalidates receipt", "Deleting verified file invalidates its receipt.", contract_refs=("G01", "D5")),
    _slot("G01-07-reverify-restores-freshness", "G01", "Re-running verification after edit restores freshness", "Fresh post-edit verification receipt satisfies closeout gate.", contract_refs=("G01", "D5")),
)

G02_SLOTS: tuple[GB07SlotSpec, ...] = (
    _slot("G02-01-read-never-substitutes-for-verification", "G02", "File read never substitutes for verification receipt", "Offering read#1 in place of verification receipt is refused.", contract_refs=("G02", "D5")),
    _slot("G02-02-diff-never-substitutes-for-verification", "G02", "Diff never substitutes for verification receipt", "Offering diff#1 in place of verification receipt is refused.", contract_refs=("G02", "D5")),
    _slot("G02-03-review-verdict-never-substitutes-for-tests", "G02", "Review verdict never substitutes for test receipt", "Offering review verdict in place of command verification is refused.", contract_refs=("G02", "D5")),
    _slot("G02-04-surface-capture-never-substitutes-for-code-read", "G02", "Surface capture never substitutes for code-line proof", "Offering screenshot/surface artifact in place of read#1 is refused.", contract_refs=("G02", "D5")),
)

G03_SLOTS: tuple[GB07SlotSpec, ...] = (
    _slot("G03-01-open-verification-gate-blocks", "G03", "Open verification gate blocks closeout with code", "Open verification gate blocks closeout regardless of closing prose.", contract_refs=("G03", "D5")),
    _slot("G03-02-open-review-gate-blocks", "G03", "Open review gate blocks closeout with code", "Open review gate blocks closeout regardless of closing prose.", contract_refs=("G03", "D5")),
    _slot("G03-03-open-worker-leg-gate-blocks", "G03", "Open active worker leg blocks closeout with code", "Incomplete worker leg blocks closeout regardless of closing prose.", contract_refs=("G03", "D5")),
    _slot("G03-04-open-finding-resolution-gate-blocks", "G03", "Unresolved blocking finding blocks closeout", "Unresolved blocking finding prevents closeout.", contract_refs=("G03", "D5")),
    _slot("G03-05-open-dirty-workspace-gate-blocks", "G03", "Unverified workspace mutation blocks closeout", "Unverified workspace mutation blocks closeout with typed code.", contract_refs=("G03", "D5")),
    _slot("G03-06-refused-run-report-never-accepted", "G03", "Refused run report is never recorded as accepted", "Refused run report stays refused and is never persisted as accepted.", contract_refs=("G03", "D5")),
    _slot("G03-07-open-artifact-gate-blocks", "G03", "Missing required deliverable artifact blocks closeout", "Missing deliverable artifact blocks closeout with typed code.", contract_refs=("G03", "D5")),
    _slot("G03-08-all-gates-closed-settles", "G03", "All gates satisfied allows clean closeout", "Closing all required gates allows grounded closeout to settle.", contract_refs=("G03", "D5")),
)

G04_SLOTS: tuple[GB07SlotSpec, ...] = (
    _slot("G04-01-friction-within-intent-accumulates", "G04", "Shared friction accumulates within one intent", "Repair and withheld notes charge shared friction within an intent.", contract_refs=("G04", "D10", "D5")),
    _slot("G04-02-friction-across-phases-tracks-cycle", "G04", "Friction tracks across phases without losing cycle count", "Cross-phase friction respects cycle budget.", contract_refs=("G04", "D10", "D5")),
    _slot("G04-03-shipped-breaker-trips-at-3-total", "G04", "Shipped-mode breaker trips at 3 total ungrounded warnings", "Three total ungrounded warnings trip breaker and stay tripped (COV-07).", contract_refs=("G04", "COV-07", "D5")),
    _slot("G04-04-shipped-breaker-trips-at-2-consecutive", "G04", "Shipped-mode breaker trips at 2 consecutive ungrounded warnings", "Two consecutive ungrounded warnings trip breaker and stay tripped (COV-07).", contract_refs=("G04", "COV-07", "D5")),
    _slot("G04-05-shipped-breaker-stays-tripped-after-clean-turn", "G04", "Tripped breaker stays tripped after subsequent clean turn", "Once tripped, breaker remains tripped until explicit reset.", contract_refs=("G04", "COV-07", "D5")),
    _slot("G04-06-inprocess-block-mode-3-total-escalates", "G04", "In-process D8 block mode returns COORDINATOR_GROUNDING_ESCALATED at 3 total", "D8 in-process block mode trips at 3 total and returns COORDINATOR_GROUNDING_ESCALATED (COV-07).", lane="in_process", expected_code="COORDINATOR_GROUNDING_ESCALATED", contract_refs=("G04", "D8", "COV-07", "D5")),
    _slot("G04-07-inprocess-block-mode-2-consecutive-escalates", "G04", "In-process D8 block mode returns COORDINATOR_GROUNDING_ESCALATED at 2 consecutive", "D8 in-process block mode trips at 2 consecutive and returns COORDINATOR_GROUNDING_ESCALATED (COV-07).", lane="in_process", expected_code="COORDINATOR_GROUNDING_ESCALATED", contract_refs=("G04", "D8", "COV-07", "D5")),
    _slot("G04-08-inprocess-block-mode-stays-tripped", "G04", "In-process D8 block mode stays tripped after escalation", "Block-mode breaker stays tripped on subsequent turns.", lane="in_process", expected_code="COORDINATOR_GROUNDING_ESCALATED", contract_refs=("G04", "D8", "COV-07", "D5")),
    _slot("G04-09-inprocess-ambient-escalation-code", "G04", "In-process ambient escalation emits AMBIENT_GROUNDING_ESCALATED", "Ambient breaker escalation emits AMBIENT_GROUNDING_ESCALATED.", lane="in_process", expected_code="AMBIENT_GROUNDING_ESCALATED", contract_refs=("G04", "D8", "D5")),
    _slot("G04-10-inprocess-presentation-repair-zero-friction", "G04", "In-process presentation repair charges zero friction_units", "Presentation repair increments retry_count without charging shared friction_units.", lane="in_process", contract_refs=("G04", "D10", "D5")),
    _slot("G04-11-inprocess-shared-friction-budget-exhaustion", "G04", "In-process shared friction budget exhaustion enforces cap", "Shared friction counter enforces D8 cycle/prompt cap accurately.", lane="in_process", contract_refs=("G04", "D8", "D10", "D5")),
)

G05_SLOTS: tuple[GB07SlotSpec, ...] = (
    _slot("G05-01-search-after-compaction-finds-digest", "G05", "Search evidence after compaction locates compacted record", "Search projection locates compacted evidence by handle/digest.", contract_refs=("G05", "D5")),
    _slot("G05-02-rebuild-projection-changes-no-record", "G05", "Rebuilding search projection mutates zero evidence records", "Rebuilding FTS/search projection leaves all evidence_records digests identical.", contract_refs=("G05", "D5")),
    _slot("G05-03-search-hit-never-grants-read-proof", "G05", "Search projection hit never grants file-line read proof", "Search hit alone does not authorize line/excerpt citation without recall/read.", contract_refs=("G05", "V05", "D5")),
    _slot("G05-04-rebuild-projection-idempotent", "G05", "Repeated search projection rebuilds are idempotent", "Two consecutive rebuilds yield identical projection and evidence hashes.", contract_refs=("G05", "D5")),
)

ALL_GB07_SLOTS: tuple[GB07SlotSpec, ...] = G01_SLOTS + G02_SLOTS + G03_SLOTS + G04_SLOTS + G05_SLOTS
SLOTS_BY_SELECTOR: dict[str, GB07SlotSpec] = {s.selector: s for s in ALL_GB07_SLOTS}


def validate_gb07_catalog() -> dict[str, int]:
    if len(SLOTS_BY_SELECTOR) != len(ALL_GB07_SLOTS):
        raise ValueError("Duplicate GB07 selector detected")
    ledger = {f.family_id: f for f in FIXED_FAMILIES if f.card == "GB07"}
    total_sidecar = 0
    total_inproc = 0
    for fam_id, meta in ledger.items():
        fam_slots = [s for s in ALL_GB07_SLOTS if s.family_id == fam_id]
        sc = sum(1 for s in fam_slots if s.lane == "sidecar")
        ip = sum(1 for s in fam_slots if s.lane == "in_process")
        if sc != meta.sidecar_count or ip != meta.in_process_count:
            raise ValueError(f"{fam_id} mismatch: catalog=({sc}, {ip}) vs ledger=({meta.sidecar_count}, {meta.in_process_count})")
        total_sidecar += sc
        total_inproc += ip
    base = total_sidecar + total_inproc
    return {
        "sidecar_slots": total_sidecar,
        "in_process_slots": total_inproc,
        "base_slots": base,
        "d5_tripled_per_platform": base * len(WORDING_VARIANTS),
    }
