"""Explicit selector manifest for the 30 grounding bug-bash families.

Covers:
- All 30 families in ledger.md (26 fixed + 4 generated)
- Exact fixed slot counts: 171 sidecar + 30 in-process = 201 base,
  431 with D5 tripling per platform, 862 across both platforms
- Exact generated instance counts: 23,000 in-process + 700 sidecar = 23,700
  per platform per seed set across base seeds 20261127 and 3bb1464d
- All 7 preflight contract coverage rows from 002-preflight-coverage.md
- GB01 findings F1, F2, and F3 from 004-gb01-review-gb02-prep.md
"""
from __future__ import annotations

from dataclasses import dataclass


PLATFORMS: tuple[str, str] = ("macos", "linux")
BASE_SEEDS: tuple[str, str] = ("20261127", "3bb1464d")
WORDING_VARIANTS: tuple[str, str, str] = ("confident", "hedged", "completion_asserting")


@dataclass(frozen=True)
class FamilySelector:
    family_id: str
    track: str
    card: str
    lane: str  # "sidecar" | "in_process" | "both"
    generated: bool
    d5_tripled: bool
    sidecar_count: int
    in_process_count: int
    description: str

    @property
    def base_count(self) -> int:
        return self.sidecar_count + self.in_process_count

    @property
    def per_platform_runs(self) -> int:
        if self.generated:
            return self.base_count
        return self.base_count * (3 if self.d5_tripled else 1)


@dataclass(frozen=True)
class PreflightCoverageRow:
    row_id: str
    families: tuple[str, ...]
    assertions: tuple[str, ...]
    summary: str


@dataclass(frozen=True)
class GB01FindingAssertion:
    finding_id: str
    families: tuple[str, ...]
    assertions: tuple[str, ...]
    summary: str


FIXED_FAMILIES: tuple[FamilySelector, ...] = (
    # Evidence track (GB03)
    FamilySelector("V01", "evidence", "GB03", "sidecar", False, False, 14, 0, "Minting"),
    FamilySelector("V03", "evidence", "GB03", "sidecar", False, False, 8, 0, "Supersession and history"),
    FamilySelector("V04", "evidence", "GB03", "sidecar", False, False, 10, 0, "Command observations (+1 Decision 014 copied second-file tag)"),
    FamilySelector("V05", "evidence", "GB03", "sidecar", False, False, 5, 0, "Compaction and recall"),
    FamilySelector("V06", "evidence", "GB03", "sidecar", False, False, 10, 0, "Scope and provenance"),
    FamilySelector("V07", "evidence", "GB03", "both", False, False, 2, 4, "Restart and an unreadable ledger"),
    # Resolution track (GB04)
    FamilySelector("R01", "resolution", "GB04", "sidecar", False, False, 10, 0, "Citation types"),
    FamilySelector("R02", "resolution", "GB04", "in_process", False, False, 0, 8, "The five verdicts"),
    FamilySelector("R03", "resolution", "GB04", "in_process", False, False, 0, 10, "Coordinates and excerpts"),
    FamilySelector("R05", "resolution", "GB04", "sidecar", False, False, 6, 0, "Rejection feedback"),
    # Surfaces track (GB05, D5 tripled)
    FamilySelector("S01", "surfaces", "GB05", "sidecar", False, True, 5, 0, "When citations are required"),
    FamilySelector("S02", "surfaces", "GB05", "sidecar", False, True, 4, 0, "Host attachment"),
    FamilySelector("S03", "surfaces", "GB05", "sidecar", False, True, 9, 0, "Citation repair"),
    FamilySelector("S04", "surfaces", "GB05", "sidecar", False, True, 8, 0, "Report documents"),
    FamilySelector("S05", "surfaces", "GB05", "sidecar", False, True, 6, 0, "Presentation repair"),
    FamilySelector("S06", "surfaces", "GB05", "both", False, True, 13, 2, "Mid-turn notes (+1 Decision 014 historical unknown intent boundary)"),
    FamilySelector("S07", "surfaces", "GB05", "sidecar", False, True, 4, 0, "What a closeout may cite"),
    # Writes and verdicts track (GB06, D5 tripled)
    FamilySelector("W01", "writes", "GB06", "sidecar", False, True, 6, 0, "Findings"),
    FamilySelector("W02", "writes", "GB06", "sidecar", False, True, 9, 0, "Review verdicts"),
    FamilySelector("W03", "writes", "GB06", "sidecar", False, True, 9, 0, "Worker completion"),
    FamilySelector("W04", "writes", "GB06", "sidecar", False, True, 7, 0, "Surface claims"),
    # Freshness and gates track (GB07, D5 tripled)
    FamilySelector("G01", "freshness", "GB07", "sidecar", False, True, 7, 0, "Verification freshness"),
    FamilySelector("G02", "freshness", "GB07", "sidecar", False, True, 4, 0, "Proof layers do not substitute"),
    FamilySelector("G03", "freshness", "GB07", "sidecar", False, True, 8, 0, "Closeout gates"),
    FamilySelector("G04", "freshness", "GB07", "both", False, True, 5, 6, "Friction and the breaker"),
    FamilySelector("G05", "freshness", "GB07", "sidecar", False, True, 4, 0, "Search as a projection"),
)

GENERATED_FAMILIES: tuple[FamilySelector, ...] = (
    FamilySelector("V02", "evidence", "GB03", "both", True, False, 100, 3000, "Tool output imitating host structure"),
    FamilySelector("R04", "resolution", "GB04", "both", True, False, 200, 10000, "Markdown with citations and look-alikes"),
    FamilySelector("R06", "resolution", "GB04", "both", True, False, 200, 5000, "Delivery log and a supported citation"),
    FamilySelector("R07", "resolution", "GB04", "both", True, False, 200, 5000, "Delivery log and a near-truth citation"),
)

ALL_FAMILIES: tuple[FamilySelector, ...] = FIXED_FAMILIES + GENERATED_FAMILIES

# All 7 coverage rows from coordination/decisions/002-preflight-coverage.md
PREFLIGHT_COVERAGE_ROWS: tuple[PreflightCoverageRow, ...] = (
    PreflightCoverageRow(
        row_id="COV-01",
        families=("R03", "R06", "R07"),
        assertions=(
            "reanchor_unique_excerpt_wrong_line",
            "reanchor_zero_matches_preserves_rejection",
            "reanchor_multiple_matches_preserves_rejection",
            "stale_ordinal_binding_distinct_from_wrong_leg_or_observation",
        ),
        summary="Unique-excerpt re-anchoring (0, 1, >1 matches) and stale-ordinal binding vs wrong-leg/wrong-observation rejection.",
    ),
    PreflightCoverageRow(
        row_id="COV-02",
        families=("R04", "GB08"),
        assertions=(
            "indented_code_is_literal",
            "known_vs_unknown_bare_path",
            "group_disposition",
            "pathless_handle_and_control_directive_removal_without_markdown_structure",
        ),
        summary="Indented code, known vs unknown bare path, group disposition, and presentation cleanup without creating Markdown structure.",
    ),
    PreflightCoverageRow(
        row_id="COV-03",
        families=("S03",),
        assertions=(
            "missing_handle_never_repeats_effect",
            "operational_batch_retires_pinned_draft_retaining_budgets",
            "terminal_worker_card_publication_retires_pinned_draft_even_on_failure",
            "typed_coordinator_wake_retires_pinned_draft",
            "denied_batch_admission_retires_pinned_draft",
        ),
        summary="Pinned draft retirement triggers and budget retention in S03 citation repair.",
    ),
    PreflightCoverageRow(
        row_id="COV-04",
        families=("S04",),
        assertions=(
            "unwrap_only_sole_markdown_or_md_fence",
            "bare_json_or_empty_closeout_returns_COORDINATOR_CLOSEOUT_NO_ANSWER",
            "strict_trailing_report_json_fence",
            "tool_availability_preserved_on_repair_vs_final_prose_only_turn",
        ),
        summary="Sole markdown/md fence unwrapping, COORDINATOR_CLOSEOUT_NO_ANSWER, strict report JSON, and repair tool availability.",
    ),
    PreflightCoverageRow(
        row_id="COV-05",
        families=("S06",),
        assertions=(
            "source_change_proves_only_brief_row",
            "source_change_creates_no_citation_duty",
            "source_change_supplies_no_line_or_excerpt",
            "source_change_unavailable_to_recall",
            "derived_user_note_creates_no_second_model_visible_copy",
            "S06-historical-unknown-intent-boundary",
        ),
        summary="source_change# brief-only semantics, derived user note exclusion, and Decision 008 S06 pre/post-batch intent-window boundary.",
    ),
    PreflightCoverageRow(
        row_id="COV-06",
        families=("W04",),
        assertions=(
            "held_interactive_terminal_snapshot_supported_claims",
            "console_or_process_log_supported_claims",
            "sealed_command_terminal_capture_supported_claims",
            "artifact_identity_provenance_host_vs_fixture_payload",
        ),
        summary="Held interactive terminal snapshot, console/process log, sealed command capture, and artifact identity provenance.",
    ),
    PreflightCoverageRow(
        row_id="COV-07",
        families=("G04",),
        assertions=(
            "breaker_threshold_3_total_ungrounded",
            "breaker_threshold_2_consecutive_ungrounded",
            "in_process_block_policy_returns_COORDINATOR_GROUNDING_ESCALATED",
        ),
        summary="Breaker thresholds (3 total, 2 consecutive) and COORDINATOR_GROUNDING_ESCALATED under D8 in-process block policy.",
    ),
)

# GB01 findings F1, F2, F3 from coordination/decisions/004-gb01-review-gb02-prep.md
GB01_FINDINGS: tuple[GB01FindingAssertion, ...] = (
    GB01FindingAssertion(
        finding_id="F1",
        families=("W03",),
        assertions=(
            "active_missing_artifact_outcome_WORKER_SUMMARY_NO_ARTIFACT",
            "retain_deprecated_WORKER_IMPLEMENT_NO_ARTIFACT_compatibility_evidence",
            "keep_implementer_role_scenario_and_ledger_slot",
        ),
        summary="WORKER_SUMMARY_NO_ARTIFACT is active; retain deprecated WORKER_IMPLEMENT_NO_ARTIFACT as compatibility evidence.",
    ),
    GB01FindingAssertion(
        finding_id="F2",
        families=("V01", "R04"),
        assertions=(
            "inventory_dialed_host_retrieved_host_audited_host_together",
            "contact_records_alone_never_grant_url_citation_support",
            "model_visible_distinction_for_mediated_contacts",
        ),
        summary="dialed_host#, retrieved_host#, and audited_host# are mediated contact records and never grant URL citation support alone.",
    ),
    GB01FindingAssertion(
        finding_id="F3",
        families=("V04", "R03", "R06", "R07"),
        assertions=(
            "contrast_correctly_numbered_lines_matched",
            "contrast_unnumbered_delivery_traced",
            "contrast_mismatched_printed_numbers_without_excerpt_product_unverifiable_vs_contract_traced",
            "contrast_mismatched_printed_numbers_with_verified_excerpt_grounded",
            "V04-copied-source-tag-second-file",
            "V04-displayed-continuous-numbers",
            "V04-rejected-inferred-coordinate",
            "V04-uniquely-matching-excerpt",
        ),
        summary="Explicit F3 and Decision 005 contrast cases keeping D1 product behavior, copied host tags, and contract reading distinct.",
    ),
)

# Decision 005 explicit V04 sidecar assertion & contrast set for multi-file `cat -n a.go b.go`
DECISION_005_V04_ASSERTIONS: tuple[str, ...] = (
    "V04-copied-source-tag-second-file",
    "V04-displayed-continuous-numbers",
    "V04-rejected-inferred-coordinate",
    "V04-uniquely-matching-excerpt",
)

# Decision 008 synthetic S06 boundary assertion (preserves original `unknown` in GB01 history)
DECISION_008_S06_ASSERTIONS: tuple[str, ...] = (
    "S06-historical-unknown-intent-boundary",
)


def manifest_summary() -> dict[str, int]:
    """Compute exact ledger counts across all 30 families."""
    fixed_sidecar = sum(f.sidecar_count for f in FIXED_FAMILIES)
    fixed_in_process = sum(f.in_process_count for f in FIXED_FAMILIES)
    fixed_base = fixed_sidecar + fixed_in_process
    fixed_tripled_per_platform = sum(f.per_platform_runs for f in FIXED_FAMILIES)
    fixed_both_platforms = fixed_tripled_per_platform * len(PLATFORMS)

    gen_in_process = sum(f.in_process_count for f in GENERATED_FAMILIES)
    gen_sidecar = sum(f.sidecar_count for f in GENERATED_FAMILIES)
    gen_per_platform_per_seed = gen_in_process + gen_sidecar

    return {
        "total_families": len(ALL_FAMILIES),
        "fixed_families": len(FIXED_FAMILIES),
        "generated_families": len(GENERATED_FAMILIES),
        "fixed_sidecar_slots": fixed_sidecar,
        "fixed_in_process_slots": fixed_in_process,
        "fixed_base_slots": fixed_base,
        "fixed_tripled_per_platform": fixed_tripled_per_platform,
        "fixed_both_platforms": fixed_both_platforms,
        "generated_in_process_per_platform_per_seed": gen_in_process,
        "generated_sidecar_per_platform_per_seed": gen_sidecar,
        "generated_per_platform_per_seed": gen_per_platform_per_seed,
        "preflight_coverage_rows": len(PREFLIGHT_COVERAGE_ROWS),
        "gb01_findings": len(GB01_FINDINGS),
    }


def validate_manifest() -> dict[str, int]:
    """Assert every ledger, preflight coverage, and GB01 finding invariant."""
    summary = manifest_summary()
    if len({f.family_id for f in ALL_FAMILIES}) != 30:
        raise ValueError("Expected exactly 30 unique families")
    expected = {
        "total_families": 30,
        "fixed_families": 26,
        "generated_families": 4,
        "fixed_sidecar_slots": 173,
        "fixed_in_process_slots": 30,
        "fixed_base_slots": 203,
        "fixed_tripled_per_platform": 435,
        "fixed_both_platforms": 870,
        "generated_in_process_per_platform_per_seed": 23000,
        "generated_sidecar_per_platform_per_seed": 700,
        "generated_per_platform_per_seed": 23700,
        "preflight_coverage_rows": 7,
        "gb01_findings": 3,
    }
    for key, val in expected.items():
        if summary[key] != val:
            raise ValueError(f"Manifest count mismatch for {key}: got {summary[key]}, expected {val}")
    return summary
