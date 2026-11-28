"""Explicit selector-to-assertion manifest for GB04 (Citation resolution).

Covers every GB04 fixed slot and generated family specified in:
- ledger.md (R01=10, R02=8, R03=10, R05=6 -> 34 fixed slots; R04, R06, R07 generated)
- coordination/decisions/002-preflight-coverage.md (COV-01, COV-02)
- coordination/decisions/004-gb01-review-gb02-prep.md (F2, F3)
- coordination/decisions/018-release-gb03-gb04-fixture-prep.md (separate syntax/scope/support,
  explicit exact-verdict assertions on R02/R03).
"""
from __future__ import annotations

from dataclasses import dataclass

from grounding.generators import BASE_SEEDS, R06_VARIATIONS, R07_MUTATIONS
from grounding.selectors import PLATFORMS


GB04_BASE_COMMIT: str = "581ce2c0ec2fd5916bb2e032008fb52d5411c92d"
GB04_FIXED_FAMILIES: tuple[str, ...] = ("R01", "R02", "R03", "R05")
GB04_GENERATED_FAMILIES: tuple[str, ...] = ("R04", "R06", "R07")
GB04_ALL_FAMILIES: tuple[str, ...] = ("R01", "R02", "R03", "R04", "R05", "R06", "R07")


@dataclass(frozen=True)
class GB04SlotSpec:
    """Explicit selector-to-assertion entry for one fixed GB04 slot."""

    selector: str
    family_id: str
    lane: str  # "sidecar" | "in_process"
    assert_exact_verdicts: bool
    citation_types: tuple[str, ...]
    assertions: tuple[str, ...]
    expected_verdicts: tuple[str, ...]
    contract_refs: tuple[str, ...]
    summary: str
    # R05 feedback-specific expectations (None for non-R05 slots)
    expected_refusal_reason: str | None = None
    expected_suggested_replacement: str | None = None
    host_rewrites_citation: bool = False


@dataclass(frozen=True)
class GB04GeneratedFamilySpec:
    """Explicit manifest entry for one generated GB04 family (R04, R06, R07)."""

    family_id: str
    lane: str  # "both"
    in_process_count: int
    sidecar_count: int
    base_seeds: tuple[str, ...]
    platforms: tuple[str, ...]
    assertions: tuple[str, ...]
    variations_or_mutations: tuple[str, ...]
    contract_refs: tuple[str, ...]
    summary: str

    @property
    def per_platform_per_seed(self) -> int:
        return self.in_process_count + self.sidecar_count


# ---------------------------------------------------------------------------
# R01 · Citation types (10 sidecar slots)
# ---------------------------------------------------------------------------
R01_SLOTS: tuple[GB04SlotSpec, ...] = (
    GB04SlotSpec(
        selector="R01-01-handle-supported",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle",),
        assertions=("evidence_handle_supported_in_scope",),
        expected_verdicts=("matched",),
        contract_refs=("R01", "D3"),
        summary="Supported evidence handle [read#1] resolves against in-scope delivery.",
    ),
    GB04SlotSpec(
        selector="R01-02-handle-unsupported",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle",),
        assertions=("evidence_handle_unknown_rejected",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R01", "D3"),
        summary="Unsupported evidence handle [read#99] not present in delivery log is refused.",
    ),
    GB04SlotSpec(
        selector="R01-03-path-coord-supported",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("path_coord",),
        assertions=("path_with_line_and_excerpt_supported",),
        expected_verdicts=("matched",),
        contract_refs=("R01", "R03"),
        summary="Supported path+line/excerpt citation resolves against current file observation.",
    ),
    GB04SlotSpec(
        selector="R01-04-path-coord-unsupported",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("path_coord",),
        assertions=("unobserved_path_and_uncovered_line_rejected",),
        expected_verdicts=("unverifiable", "unverifiable"),
        contract_refs=("R01", "R03"),
        summary="Unsupported path+line citations (unobserved path and uncovered line) are refused.",
    ),
    GB04SlotSpec(
        selector="R01-05-handle-path-supported",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle_path",),
        assertions=("handle_with_matching_path_supported",),
        expected_verdicts=("matched",),
        contract_refs=("R01",),
        summary="Supported handle+path citation matching the observation path resolves.",
    ),
    GB04SlotSpec(
        selector="R01-06-handle-path-contradictory",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle_path",),
        assertions=("handle_with_contradictory_path_rejected",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R01", "V03"),
        summary="Handle combined with a contradictory path is refused even if another record observed that path.",
    ),
    GB04SlotSpec(
        selector="R01-07-url-observed-admitted",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("url",),
        assertions=("url_observed_via_admitted_tool_supported",),
        expected_verdicts=("matched",),
        contract_refs=("R01", "GB01-F2"),
        summary="URL observed through an admitted tool path (web_fetch / web#) resolves.",
    ),
    GB04SlotSpec(
        selector="R01-08-url-unobserved-contact-only",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("url",),
        assertions=(
            "contact_records_alone_never_grant_url_citation_support",
            "unobserved_url_rejected",
        ),
        expected_verdicts=("unverifiable", "unverifiable"),
        contract_refs=("R01", "GB01-F2"),
        summary="URL recorded only via mediated contact record (dialed_host#) or never observed is refused.",
    ),
    GB04SlotSpec(
        selector="R01-09-artifact-present-permitted",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("artifact",),
        assertions=("artifact_present_and_permitted_supported",),
        expected_verdicts=("matched",),
        contract_refs=("R01", "GB02-R4"),
        summary="Artifact ID present in scope and in permitted_artifacts resolves.",
    ),
    GB04SlotSpec(
        selector="R01-10-artifact-denied-or-absent",
        family_id="R01",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("artifact",),
        assertions=(
            "artifact_denied_by_empty_permitted_set_rejected",
            "artifact_absent_rejected",
        ),
        expected_verdicts=("unverifiable", "unverifiable"),
        contract_refs=("R01", "GB02-R4"),
        summary="Artifact ID denied by permitted_artifacts=frozenset() or absent from delivery log is refused.",
    ),
)

# ---------------------------------------------------------------------------
# R02 · The five verdicts (8 in-process slots, assert_exact_verdicts=True)
# ---------------------------------------------------------------------------
R02_SLOTS: tuple[GB04SlotSpec, ...] = (
    GB04SlotSpec(
        selector="R02-01-verdict-matched",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("exact_verdict_matched",),
        expected_verdicts=("matched",),
        contract_refs=("R02", "D4", "D018", "D047", "D066"),
        summary="Exact handle and line-numbered coordinate (in-process excerpt-free citation expects `traced` per Decisions 047 & 066).",
    ),
    GB04SlotSpec(
        selector="R02-02-verdict-traced",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("exact_verdict_traced",),
        expected_verdicts=("traced",),
        contract_refs=("R02", "R03", "D4", "D018"),
        summary="Coordinate inside an unnumbered delivered range without excerpt yields exact verdict `traced`.",
    ),
    GB04SlotSpec(
        selector="R02-03-verdict-bound-unique",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("path_coord",),
        assertions=("exact_verdict_bound_unique_satisfying_record",),
        expected_verdicts=("bound",),
        contract_refs=("R02", "D4", "D018", "D047", "D105"),
        summary="Normalized path without excerpt yields exact verdict `traced` in-process (same as non-normalized equivalent per Decisions 047 & 105).",
    ),
    GB04SlotSpec(
        selector="R02-04-verdict-ambiguous-two",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("path_coord",),
        assertions=("exact_verdict_ambiguous_two_satisfying_records", "host_never_picks_one"),
        expected_verdicts=("ambiguous",),
        contract_refs=("R02", "D4", "D018", "D047", "D105"),
        summary="Two current records with identical delivered bytes stay grounded (`traced`) in-process per Decisions 047 & 105.",
    ),
    GB04SlotSpec(
        selector="R02-05-verdict-unverifiable",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("exact_verdict_unverifiable",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R02", "D4", "D018"),
        summary="Unknown handle with no verifying record yields exact verdict `unverifiable`.",
    ),
    GB04SlotSpec(
        selector="R02-06-stale-ordinal-binds-uniquely",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle_path",),
        assertions=(
            "stale_ordinal_binding_distinct_from_wrong_leg_or_observation",
            "exact_verdict_bound_unique_satisfying_record",
        ),
        expected_verdicts=("bound",),
        contract_refs=("R02", "COV-01", "D018"),
        summary="Stale ordinal whose path, line, and excerpt uniquely match 1 current record yields `bound`.",
    ),
    GB04SlotSpec(
        selector="R02-07-wrong-leg-rejected",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=(
            "stale_ordinal_binding_distinct_from_wrong_leg_or_observation",
            "wrong_leg_handle_never_bound",
        ),
        expected_verdicts=("unverifiable", "unverifiable"),
        contract_refs=("R02", "COV-01", "GB02-R3", "D018"),
        summary="Handle belonging to an undispatched/wrong worker leg yields `unverifiable` (never bound).",
    ),
    GB04SlotSpec(
        selector="R02-08-wrong-observation-rejected",
        family_id="R02",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle_path", "handle"),
        assertions=(
            "stale_ordinal_binding_distinct_from_wrong_leg_or_observation",
            "wrong_observation_never_substituted",
        ),
        expected_verdicts=("unverifiable", "unverifiable"),
        contract_refs=("R02", "COV-01", "V03", "D018"),
        summary="Valid handle paired with path or excerpt from a different observation yields `unverifiable`.",
    ),
)

# ---------------------------------------------------------------------------
# R03 · Coordinates and excerpts (10 in-process slots, assert_exact_verdicts=True)
# ---------------------------------------------------------------------------
R03_SLOTS: tuple[GB04SlotSpec, ...] = (
    GB04SlotSpec(
        selector="R03-01-multiline-contiguous-blank-lines-clipping",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=(
            "multiline_contiguous_excerpt_single_observation",
            "blank_lines_preserved",
            "clipped_first_and_last_lines_preserved",
        ),
        expected_verdicts=("matched",),
        contract_refs=("R03", "D018"),
        summary="Multiline contiguous excerpt preserving an interior blank line and clipped edges yields `matched`.",
    ),
    GB04SlotSpec(
        selector="R03-02-cross-observation-bytes-refused",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("cross_observation_bytes_never_complete_excerpt",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R03", "D018"),
        summary="Excerpt splicing bytes from two separate observations yields `unverifiable`.",
    ),
    GB04SlotSpec(
        selector="R03-03-working-tree-bytes-refused",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("path_coord",),
        assertions=("working_tree_bytes_never_complete_excerpt",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R03", "D018"),
        summary="Excerpt relying on undelivered working-tree bytes yields `unverifiable`.",
    ),
    GB04SlotSpec(
        selector="R03-04-printed-line-number-and-verified-excerpt-matched",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=(
            "contrast_correctly_numbered_lines_matched",
            "verified_excerpt_on_unnumbered_delivery_matched",
        ),
        expected_verdicts=("matched", "matched"),
        contract_refs=("R03", "GB01-F3", "D018"),
        summary="Printed line number and verified excerpt on unnumbered delivery both yield exact verdict `matched`.",
    ),
    GB04SlotSpec(
        selector="R03-05-unnumbered-delivered-range-traced",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("contrast_unnumbered_delivery_traced",),
        expected_verdicts=("traced",),
        contract_refs=("R03", "GB01-F3", "D018"),
        summary="Coordinate inside an unnumbered delivered range with no excerpt yields exact verdict `traced`.",
    ),
    GB04SlotSpec(
        selector="R03-06-reanchor-unique-one-match",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("reanchor_unique_excerpt_wrong_line",),
        expected_verdicts=("matched",),
        contract_refs=("R03", "COV-01", "GB02-R1", "D018"),
        summary="Wrong single-point line with uniquely matching excerpt re-anchors to true line with `matched`.",
    ),
    GB04SlotSpec(
        selector="R03-07-reanchor-zero-matches-rejected",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("reanchor_zero_matches_preserves_rejection",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R03", "COV-01", "D018"),
        summary="Wrong line with excerpt matching 0 lines preserves `unverifiable` rejection.",
    ),
    GB04SlotSpec(
        selector="R03-08-reanchor-multiple-matches-rejected",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("reanchor_multiple_matches_preserves_rejection",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R03", "COV-01", "D018"),
        summary="Wrong line with excerpt matching >1 lines preserves `unverifiable` rejection (no silent pick).",
    ),
    GB04SlotSpec(
        selector="R03-09-inclusive-multiline-range-uncovered-end-rejected",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=("inclusive_multiline_range_uncovered_end_rejected",),
        expected_verdicts=("unverifiable",),
        contract_refs=("R03", "GB02-R6", "D018"),
        summary="Inclusive multi-line range whose end line is outside delivered_ranges is rejected (`unverifiable`).",
    ),
    GB04SlotSpec(
        selector="R03-10-f3-printed-other-numbers-contrast",
        family_id="R03",
        lane="in_process",
        assert_exact_verdicts=True,
        citation_types=("handle",),
        assertions=(
            "contrast_mismatched_printed_numbers_without_excerpt_product_unverifiable_vs_contract_traced",
            "contrast_mismatched_printed_numbers_with_verified_excerpt_grounded",
            "V04-copied-source-tag-second-file",
        ),
        expected_verdicts=("unverifiable", "matched", "matched"),
        contract_refs=("R03", "GB01-F3", "D005", "D018"),
        summary="F3 PrintedOtherNumbers contrast: unexcerpted coord (product unverifiable vs contract traced), verified excerpt (matched), copied host tag (matched).",
    ),
)

# ---------------------------------------------------------------------------
# R05 · Rejection feedback (6 sidecar slots)
# ---------------------------------------------------------------------------
R05_SLOTS: tuple[GB04SlotSpec, ...] = (
    GB04SlotSpec(
        selector="R05-01-refusal-unknown-handle",
        family_id="R05",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle",),
        assertions=("feedback_names_unknown_handle", "no_replacement_when_zero_verify", "host_never_rewrites"),
        expected_verdicts=("unverifiable",),
        contract_refs=("R05",),
        summary="Unknown handle refusal names `unknown_handle`, suggests no replacement, and never rewrites.",
        expected_refusal_reason="unknown_handle",
        expected_suggested_replacement=None,
        host_rewrites_citation=False,
    ),
    GB04SlotSpec(
        selector="R05-02-refusal-wrong-path",
        family_id="R05",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle_path",),
        assertions=("feedback_names_wrong_path", "no_replacement_when_zero_verify", "host_never_rewrites"),
        expected_verdicts=("unverifiable",),
        contract_refs=("R05",),
        summary="Wrong path refusal names `wrong_path`, suggests no replacement when 0 records verify, and never rewrites.",
        expected_refusal_reason="wrong_path",
        expected_suggested_replacement=None,
        host_rewrites_citation=False,
    ),
    GB04SlotSpec(
        selector="R05-03-refusal-uncovered-range",
        family_id="R05",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle",),
        assertions=("feedback_names_uncovered_range", "no_replacement_when_zero_verify", "host_never_rewrites"),
        expected_verdicts=("unverifiable",),
        contract_refs=("R05",),
        summary="Uncovered delivered range refusal names `uncovered_range`, suggests no replacement, and never rewrites.",
        expected_refusal_reason="uncovered_range",
        expected_suggested_replacement=None,
        host_rewrites_citation=False,
    ),
    GB04SlotSpec(
        selector="R05-04-refusal-excerpt-mismatch",
        family_id="R05",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle",),
        assertions=("feedback_names_excerpt_mismatch", "no_replacement_when_zero_verify", "host_never_rewrites"),
        expected_verdicts=("unverifiable",),
        contract_refs=("R05",),
        summary="Excerpt mismatch refusal names `excerpt_mismatch`, suggests no replacement, and never rewrites.",
        expected_refusal_reason="excerpt_mismatch",
        expected_suggested_replacement=None,
        host_rewrites_citation=False,
    ),
    GB04SlotSpec(
        selector="R05-05-replacement-suggested-unique-one",
        family_id="R05",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle_path",),
        assertions=(
            "feedback_names_wrong_path",
            "replacement_suggested_only_when_exactly_one_record_verifies",
            "host_never_rewrites",
        ),
        expected_verdicts=("unverifiable",),
        contract_refs=("R05",),
        summary="Refused handle+path where exactly 1 current-job record verifies suggests `read#2` without rewriting.",
        expected_refusal_reason="wrong_path",
        expected_suggested_replacement="read#2",
        host_rewrites_citation=False,
    ),
    GB04SlotSpec(
        selector="R05-06-replacement-omitted-when-ambiguous-multiple",
        family_id="R05",
        lane="sidecar",
        assert_exact_verdicts=False,
        citation_types=("handle_path",),
        assertions=(
            "feedback_names_wrong_path",
            "no_replacement_when_multiple_verify",
            "host_never_rewrites",
        ),
        expected_verdicts=("unverifiable",),
        contract_refs=("R05",),
        summary="Refused handle+path where 2 current-job records verify omits replacement suggestion and never rewrites.",
        expected_refusal_reason="wrong_path",
        expected_suggested_replacement=None,
        host_rewrites_citation=False,
    ),
)

ALL_GB04_FIXED_SLOTS: tuple[GB04SlotSpec, ...] = R01_SLOTS + R02_SLOTS + R03_SLOTS + R05_SLOTS

# ---------------------------------------------------------------------------
# Generated families: R04, R06, R07
# ---------------------------------------------------------------------------
GB04_GENERATED_SPECS: tuple[GB04GeneratedFamilySpec, ...] = (
    GB04GeneratedFamilySpec(
        family_id="R04",
        lane="both",
        in_process_count=10000,
        sidecar_count=200,
        base_seeds=BASE_SEEDS,
        platforms=PLATFORMS,
        assertions=(
            "indented_code_is_literal",
            "known_vs_unknown_bare_path",
            "group_disposition",
            "pathless_handle_and_control_directive_removal_without_markdown_structure",
            "http_link_syntax_distinct_from_destination_observation",
            "oversized_inline_node_gt_4096_bytes_structured_issue",
            "typed_syntax_scope_observed_support_separated",
        ),
        variations_or_mutations=(
            "citation_handle",
            "citation_handle_coord",
            "citation_handle_range",
            "citation_handle_excerpt",
            "citation_path_coord",
            "citation_quoted_path_coord",
            "citation_group",
            "citation_http_link",
            "citation_http_autolink",
            "citation_workspace_link",
            "citation_verify",
            "citation_artifact",
            "literal_indented_code",
            "known_vs_unknown_bare_path",
            "oversized_inline_node",
        ),
        contract_refs=("R04", "COV-02", "GB01-F2", "D018"),
        summary="Markdown citation grammar keeping typed citation syntax, scope, and observed support strictly separate.",
    ),
    GB04GeneratedFamilySpec(
        family_id="R06",
        lane="both",
        in_process_count=5000,
        sidecar_count=200,
        base_seeds=BASE_SEEDS,
        platforms=PLATFORMS,
        assertions=(
            "no_false_reject_in_r06",
            "darwin_apfs_case_insensitive_path_grounded",
            "linux_ext4_case_sensitive_path_normalization_grounded",
            "stale_ordinal_binding_distinct_from_wrong_leg_or_observation",
            "reanchor_unique_excerpt_wrong_line",
        ),
        variations_or_mutations=R06_VARIATIONS,
        contract_refs=("R06", "COV-01", "D007", "D018"),
        summary="8 must-pass supported variations with explicit darwin (APFS) vs linux (ext4) path-case expectations.",
    ),
    GB04GeneratedFamilySpec(
        family_id="R07",
        lane="both",
        in_process_count=5000,
        sidecar_count=200,
        base_seeds=BASE_SEEDS,
        platforms=PLATFORMS,
        assertions=(
            "no_false_accept_in_r07",
            "linux_ext4_path_case_mismatch_rejected",
            "darwin_apfs_vs_linux_ext4_path_case_contrast",
            "explicit_reanchor_and_binding_distinction",
        ),
        variations_or_mutations=R07_MUTATIONS,
        contract_refs=("R07", "COV-01", "D007", "D018"),
        summary="6 near-truth mutations (must not pass) with explicit re-anchoring/binding distinction and platform case contrast.",
    ),
)

SLOT_BY_SELECTOR: dict[str, GB04SlotSpec] = {slot.selector: slot for slot in ALL_GB04_FIXED_SLOTS}
GENERATED_BY_FAMILY: dict[str, GB04GeneratedFamilySpec] = {
    spec.family_id: spec for spec in GB04_GENERATED_SPECS
}


def catalog_summary() -> dict[str, int]:
    """Return exact counts for all GB04 fixed slots and generated families."""
    return {
        "R01_slots": len(R01_SLOTS),
        "R02_slots": len(R02_SLOTS),
        "R03_slots": len(R03_SLOTS),
        "R05_slots": len(R05_SLOTS),
        "total_fixed_slots": len(ALL_GB04_FIXED_SLOTS),
        "fixed_sidecar_slots": sum(1 for s in ALL_GB04_FIXED_SLOTS if s.lane == "sidecar"),
        "fixed_in_process_slots": sum(1 for s in ALL_GB04_FIXED_SLOTS if s.lane == "in_process"),
        "exact_verdict_slots": sum(1 for s in ALL_GB04_FIXED_SLOTS if s.assert_exact_verdicts),
        "generated_families": len(GB04_GENERATED_SPECS),
        "generated_in_process_per_platform_per_seed": sum(
            g.in_process_count for g in GB04_GENERATED_SPECS
        ),
        "generated_sidecar_per_platform_per_seed": sum(
            g.sidecar_count for g in GB04_GENERATED_SPECS
        ),
    }


def validate_gb04_catalog() -> dict[str, int]:
    """Validate GB04 catalog counts and exact-verdict invariants against ledger.md and D018."""
    summary = catalog_summary()
    if len(SLOT_BY_SELECTOR) != len(ALL_GB04_FIXED_SLOTS):
        raise ValueError("Duplicate selector in GB04 fixed catalog")
    expected = {
        "R01_slots": 10,
        "R02_slots": 8,
        "R03_slots": 10,
        "R05_slots": 6,
        "total_fixed_slots": 34,
        "fixed_sidecar_slots": 16,
        "fixed_in_process_slots": 18,
        "exact_verdict_slots": 18,
        "generated_families": 3,
        "generated_in_process_per_platform_per_seed": 20000,
        "generated_sidecar_per_platform_per_seed": 600,
    }
    for key, val in expected.items():
        if summary[key] != val:
            raise ValueError(f"GB04 catalog mismatch for {key}: got {summary[key]}, expected {val}")
    for slot in R02_SLOTS + R03_SLOTS:
        if not slot.assert_exact_verdicts:
            raise ValueError(f"Slot {slot.selector} must set assert_exact_verdicts=True (D018)")
    return summary
