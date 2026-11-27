"""Explicit selector-to-assertion manifest for GB06 (Findings, verdicts, worker results: W01..W04).

Matches `ledger.md`, `grounding.selectors`, Decision 002 (COV-06), Decision 004 (GB01-F1:
`WORKER_SUMMARY_NO_ARTIFACT` active while retaining deprecated `WORKER_IMPLEMENT_NO_ARTIFACT`
compatibility evidence), and D5 tripling across all 3 wordings:
- W01: 6 sidecar slots (findings from each evidence kind; unsupported finding never stored)
- W02: 9 sidecar slots (submit_verdict from each owning state and typed refusal codes)
- W03: 9 sidecar slots (worker completion per role with/without evidence, retry exhaustion,
       GB01-F1 active WORKER_SUMMARY_NO_ARTIFACT + deprecated compatibility evidence)
- W04: 7 sidecar slots (surface claims, visual evidence kinds, COV-06 terminal/console/sealed
       captures and artifact provenance)

Total: 31 sidecar slots = 31 base slots (93 per platform after D5 tripling).
"""
from __future__ import annotations

from dataclasses import dataclass

from grounding.selectors import FIXED_FAMILIES, WORDING_VARIANTS


GB06_FAMILY_IDS: tuple[str, ...] = ("W01", "W02", "W03", "W04")
GB06_BASE_COMMIT: str = "40c593ac1cc85bb31bb1a4f21812f20ab347e407"


@dataclass(frozen=True)
class GB06SlotSpec:
    selector: str
    family_id: str
    lane: str
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
    expected_code: str = "",
    contract_refs: tuple[str, ...] = (),
) -> GB06SlotSpec:
    return GB06SlotSpec(
        selector=selector,
        family_id=family_id,
        lane="sidecar",
        title=title,
        assertion=assertion,
        expected_code=expected_code,
        contract_refs=contract_refs,
    )


W01_SLOTS: tuple[GB06SlotSpec, ...] = (
    _slot("W01-01-finding-from-file-read", "W01", "Finding supported by read#1 is stored", "Finding citing verified read#1 line is stored with grounded citation.", contract_refs=("W01", "D5")),
    _slot("W01-02-finding-from-command-receipt", "W01", "Finding supported by command#1 is stored", "Finding citing verified command#1 receipt is stored.", contract_refs=("W01", "D5")),
    _slot("W01-03-finding-from-web-url", "W01", "Finding supported by observed web URL is stored", "Finding citing observed web_fetch URL is stored.", contract_refs=("W01", "D5")),
    _slot("W01-04-finding-from-artifact", "W01", "Finding supported by artifact UUID is stored", "Finding citing observed artifact UUID is stored.", contract_refs=("W01", "D5")),
    _slot("W01-05-finding-unsupported-handle-never-stored", "W01", "Unsupported handle finding is refused and never stored", "Finding citing unobserved handle returns typed refusal and zero stored row.", contract_refs=("W01", "D5")),
    _slot("W01-06-finding-unsupported-excerpt-never-stored", "W01", "Unsupported excerpt finding is refused and never stored", "Finding with mismatched excerpt returns typed refusal and zero stored row.", contract_refs=("W01", "D5")),
)

W02_SLOTS: tuple[GB06SlotSpec, ...] = (
    _slot("W02-01-verdict-approved-with-evidence", "W02", "submit_verdict approve with grounded evidence succeeds", "Approved review verdict with valid evidence settles cleanly.", contract_refs=("W02", "D5")),
    _slot("W02-02-verdict-changes-requested-with-finding", "W02", "submit_verdict changes_requested with grounded finding succeeds", "Changes-requested verdict backed by stored grounded finding settles cleanly.", contract_refs=("W02", "D5")),
    _slot("W02-03-verdict-refused-wrong-phase", "W02", "submit_verdict refused outside review phase", "Calling submit_verdict from non-review state returns owning refusal code.", contract_refs=("W02", "D5")),
    _slot("W02-04-verdict-refused-unobserved-citation", "W02", "submit_verdict refused on unobserved citation", "Verdict citing unobserved handle is refused with typed code.", contract_refs=("W02", "D5")),
    _slot("W02-05-verdict-refused-missing-findings", "W02", "submit_verdict changes_requested without findings refused", "Changes-requested verdict with no findings returns owning refusal code.", contract_refs=("W02", "D5")),
    _slot("W02-06-verdict-refused-stale-revision", "W02", "submit_verdict refused on stale revision", "Verdict referencing stale code revision is refused.", contract_refs=("W02", "D5")),
    _slot("W02-07-verdict-refused-duplicate-submission", "W02", "Duplicate submit_verdict refused from terminal state", "Second submit_verdict after terminal verdict returns owning refusal code.", contract_refs=("W02", "D5")),
    _slot("W02-08-verdict-refused-ungrounded-summary", "W02", "submit_verdict refused on ungrounded summary claim", "Verdict with ungrounded summary citation is refused.", contract_refs=("W02", "D5")),
    _slot("W02-09-verdict-refused-non-reviewer-role", "W02", "submit_verdict refused for non-reviewer role", "Non-reviewer worker invoking submit_verdict returns owning refusal code.", contract_refs=("W02", "D5")),
)

W03_SLOTS: tuple[GB06SlotSpec, ...] = (
    _slot("W03-01-investigator-complete-with-evidence", "W03", "Investigator leg completes with grounded evidence", "Investigator completes cleanly with observed read#1 evidence.", contract_refs=("W03", "D5")),
    _slot("W03-02-investigator-refused-without-evidence", "W03", "Investigator leg refused without evidence", "Investigator completion without evidence returns typed refusal.", contract_refs=("W03", "D5")),
    _slot("W03-03-implementer-complete-with-artifact", "W03", "Implementer leg completes with artifact and diff evidence", "Implementer leg completes cleanly when required artifact/diff exists.", contract_refs=("W03", "F1", "D5")),
    _slot("W03-04-implementer-missing-artifact-active-code", "W03", "Implementer missing artifact returns active WORKER_SUMMARY_NO_ARTIFACT (GB01-F1)", "Active D1 outcome is WORKER_SUMMARY_NO_ARTIFACT; deprecated WORKER_IMPLEMENT_NO_ARTIFACT retained as compatibility evidence.", expected_code="WORKER_SUMMARY_NO_ARTIFACT", contract_refs=("W03", "F1", "D5")),
    _slot("W03-05-reviewer-complete-with-verdict", "W03", "Reviewer leg completes after grounded verdict", "Reviewer leg completes cleanly after valid submit_verdict.", contract_refs=("W03", "D5")),
    _slot("W03-06-reviewer-refused-without-verdict", "W03", "Reviewer leg refused without verdict", "Reviewer leg completion without verdict returns owning refusal code.", contract_refs=("W03", "D5")),
    _slot("W03-07-verifier-complete-with-fresh-receipt", "W03", "Verifier leg completes with fresh verification receipt", "Verifier leg completes cleanly with fresh command receipt.", contract_refs=("W03", "D5")),
    _slot("W03-08-verifier-refused-without-receipt", "W03", "Verifier leg refused without verification receipt", "Verifier leg completion without receipt returns owning refusal code.", contract_refs=("W03", "D5")),
    _slot("W03-09-worker-retry-exhaustion-keeps-supported-only", "W03", "Worker retry exhaustion keeps supported claims, drops unsupported, not complete", "At retry exhaustion supported claims are retained, unsupported dropped, result not marked complete.", contract_refs=("W03", "D5")),
)

W04_SLOTS: tuple[GB06SlotSpec, ...] = (
    _slot("W04-01-screenshot-supports-visual-only", "W04", "Screenshot supports visual layout claims only", "Screenshot evidence supports stated visual claim and refuses non-visual code-line claims.", contract_refs=("W04", "D5")),
    _slot("W04-02-dom-snapshot-supports-dom-structure", "W04", "DOM snapshot supports stated DOM/accessibility claims", "DOM snapshot supports stated element/text claims only.", contract_refs=("W04", "D5")),
    _slot("W04-03-held-interactive-terminal-snapshot", "W04", "Held interactive terminal snapshot supports terminal state claims (COV-06)", "Held interactive terminal snapshot supports stated terminal output claims.", contract_refs=("W04", "COV-06", "D5")),
    _slot("W04-04-console-or-process-log-claims", "W04", "Console/process log supports log output claims (COV-06)", "Console or process log evidence supports stated log lines only.", contract_refs=("W04", "COV-06", "D5")),
    _slot("W04-05-sealed-command-terminal-capture", "W04", "Sealed command terminal capture supports captured output (COV-06)", "Sealed command terminal capture supports stated command output.", contract_refs=("W04", "COV-06", "D5")),
    _slot("W04-06-artifact-provenance-host-vs-fixture", "W04", "Artifact identity provenance separates host vs fixture payload (COV-06)", "Host-issued artifact UUID is distinct from spoofed fixture payload UUID.", contract_refs=("W04", "COV-06", "D5")),
    _slot("W04-07-visual-kind-cross-claim-refused", "W04", "Visual evidence kind refuses out-of-scope claim", "Offering visual evidence for an unstated code/network claim is refused.", contract_refs=("W04", "D5")),
)

ALL_GB06_SLOTS: tuple[GB06SlotSpec, ...] = W01_SLOTS + W02_SLOTS + W03_SLOTS + W04_SLOTS
SLOTS_BY_SELECTOR: dict[str, GB06SlotSpec] = {s.selector: s for s in ALL_GB06_SLOTS}


def validate_gb06_catalog() -> dict[str, int]:
    if len(SLOTS_BY_SELECTOR) != len(ALL_GB06_SLOTS):
        raise ValueError("Duplicate GB06 selector detected")
    ledger = {f.family_id: f for f in FIXED_FAMILIES if f.card == "GB06"}
    total_sidecar = 0
    for fam_id, meta in ledger.items():
        fam_slots = [s for s in ALL_GB06_SLOTS if s.family_id == fam_id]
        if len(fam_slots) != meta.sidecar_count or meta.in_process_count != 0:
            raise ValueError(f"{fam_id} mismatch: catalog={len(fam_slots)} vs ledger=({meta.sidecar_count}, {meta.in_process_count})")
        total_sidecar += len(fam_slots)
    return {
        "sidecar_slots": total_sidecar,
        "in_process_slots": 0,
        "base_slots": total_sidecar,
        "d5_tripled_per_platform": total_sidecar * len(WORDING_VARIANTS),
    }
