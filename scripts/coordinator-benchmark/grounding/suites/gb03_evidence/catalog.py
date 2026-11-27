"""Explicit selector-to-assertion manifest for GB03 (Evidence recording and scope).

Covers all GB03 fixed slots and generated families matching `ledger.md`,
`acceptance-plan.md` (V01..V07), Decision 005/014 (V04 = 10 sidecar slots),
and GB01 Findings F2 & F3:
- V01: 14 sidecar slots (tool categories, append-only ordinals, error receipts,
       refused/failed mutations, F2 mediated contact-record inventory)
- V02: generated (3,000 in-process / 100 sidecar per platform per seed set
       `20261127` & `3bb1464d`) — tool-output lookalikes, zero promotion
- V03: 8 sidecar slots (supersession & history, trust survival, receipt survival)
- V04: 10 sidecar slots (command observations, pipelines, 16-tag cap, opaque
       commands, F3 contrast set, Decision 005/014 `V04-10-copied-second-file-tag`)
- V05: 5 sidecar slots (compaction by digest, recall by handle, search index
       non-read, `source_change#` exclusion, D16 secret canary screening)
- V06: 10 sidecar slots (coordinator, 2 workers, 2nd chat, 2nd project,
       leg-namespaced union, round boundary, refresh/rejection invalidation)
- V07: 2 sidecar + 4 in-process slots (restart around commit/resolution,
       unreadable ledger never treated as empty, no exemption)
"""
from __future__ import annotations

from dataclasses import dataclass

from grounding.generators import BASE_SEEDS
from grounding.selectors import FIXED_FAMILIES, GENERATED_FAMILIES


GB03_FAMILY_IDS: tuple[str, ...] = ("V01", "V02", "V03", "V04", "V05", "V06", "V07")
GB03_FIXED_FAMILY_IDS: tuple[str, ...] = ("V01", "V03", "V04", "V05", "V06", "V07")


@dataclass(frozen=True)
class GB03SlotSpec:
    """Explicit selector-to-assertion manifest entry for one GB03 fixed slot."""

    selector: str
    family_id: str
    lane: str  # "sidecar" | "in_process"
    title: str
    assertion: str
    contract_refs: tuple[str, ...] = ()
    finding_refs: tuple[str, ...] = ()
    alias_selectors: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        if self.family_id not in GB03_FIXED_FAMILY_IDS:
            raise ValueError(f"Unexpected GB03 fixed family_id {self.family_id!r}")
        if self.lane not in {"sidecar", "in_process"}:
            raise ValueError(f"Unsupported lane {self.lane!r}")


@dataclass(frozen=True)
class GB03GeneratedSpec:
    """Manifest entry for the GB03 generated family (V02)."""

    family_id: str
    base_seeds: tuple[str, ...]
    in_process_per_platform_per_seed: int
    sidecar_per_platform_per_seed: int
    byte_range: tuple[int, int]
    marker_range: tuple[int, int]
    assertion: str


V02_GENERATED_SPEC = GB03GeneratedSpec(
    family_id="V02",
    base_seeds=BASE_SEEDS,
    in_process_per_platform_per_seed=3000,
    sidecar_per_platform_per_seed=100,
    byte_range=(1, 4000),
    marker_range=(1, 8),
    assertion=(
        "Tool output imitating host structure (delivery tags, handles, JSON envelopes, "
        "numbered-line prefixes, brace-heavy text) creates zero promoted evidence metadata, "
        "zero host-issued delivery tags, and zero citable coordinates."
    ),
)


# ---------------------------------------------------------------------------
# V01 · Minting (14 sidecar slots)
# ---------------------------------------------------------------------------
V01_SLOTS: tuple[GB03SlotSpec, ...] = (
    GB03SlotSpec(
        selector="V01-01-read-file-region",
        family_id="V01",
        lane="sidecar",
        title="read_file mints file_region record matching delivery",
        assertion=(
            "read_file mints read#1 with exact kind, shape=file_region, path, "
            "1-based inclusive delivered_ranges, body SHA-256, line_numbered=True, "
            "source_revision, and trust=trusted."
        ),
        contract_refs=("V01", "D3", "GB02-R2"),
    ),
    GB03SlotSpec(
        selector="V01-02-grep-find-list-stat-wc",
        family_id="V01",
        lane="sidecar",
        title="Search and filesystem metadata tools mint 1-based handles per kind",
        assertion=(
            "grep, find, list, stat, and wc each mint independent 1-based handles "
            "(grep#1, find#1, list#1, stat#1, wc#1) matching their delivered bytes and shapes."
        ),
        contract_refs=("V01", "D1-evidence-kinds"),
    ),
    GB03SlotSpec(
        selector="V01-03-edit-write-replace-restore",
        family_id="V01",
        lane="sidecar",
        title="File mutation tools mint mutation records with updated revision",
        assertion=(
            "edit, write, replace, and restore each mint 1-based handles matching "
            "their delivered post-mutation ranges, body digests, and incremented source_revision."
        ),
        contract_refs=("V01", "D1-evidence-kinds"),
    ),
    GB03SlotSpec(
        selector="V01-04-rewrite-delete-diff",
        family_id="V01",
        lane="sidecar",
        title="Structural rewrite, delete, and diff tools mint matching records",
        assertion=(
            "rewrite#1, delete#1, and diff#1 match their independent delivery log "
            "records in kind, shape, path, body digest, and source_revision."
        ),
        contract_refs=("V01", "D1-evidence-kinds"),
    ),
    GB03SlotSpec(
        selector="V01-05-append-only-ordinals",
        family_id="V01",
        lane="sidecar",
        title="Repeated calls of one kind mint strictly append-only 1-based ordinals",
        assertion=(
            "Three sequential read_file invocations mint read#1, read#2, and read#3 "
            "in strictly monotonic 1-based append-only order with no reuse or renumbering."
        ),
        contract_refs=("V01",),
    ),
    GB03SlotSpec(
        selector="V01-06-command-success-receipt",
        family_id="V01",
        lane="sidecar",
        title="Successful command mints command#1 outcome receipt",
        assertion=(
            "A zero-exit command mints command#1 matching the delivered command envelope, "
            "body digest, shape=command_result, and source_revision."
        ),
        contract_refs=("V01",),
    ),
    GB03SlotSpec(
        selector="V01-07-command-error-outcome-receipt",
        family_id="V01",
        lane="sidecar",
        title="Error command mints failure receipt never supporting success",
        assertion=(
            "A non-zero-exit command mints command#1 recording the failure outcome; "
            "citing the failure body is grounded while claiming a passing/success excerpt is unverifiable."
        ),
        contract_refs=("V01",),
    ),
    GB03SlotSpec(
        selector="V01-08-git-error-outcome-receipt",
        family_id="V01",
        lane="sidecar",
        title="Failed Git command mints failure git_receipt never supporting success",
        assertion=(
            "A failing Git operation mints git_receipt#1 recording the failure state "
            "and never supports a claim of a clean or successful Git operation."
        ),
        contract_refs=("V01",),
    ),
    GB03SlotSpec(
        selector="V01-09-refused-invocation-no-file-proof",
        family_id="V01",
        lane="sidecar",
        title="Refused tool invocation mints no file-content proof",
        assertion=(
            "A policy-refused file read mints zero file-region evidence records; "
            "any path/line citation to the refused file resolves as unverifiable."
        ),
        contract_refs=("V01",),
    ),
    GB03SlotSpec(
        selector="V01-10-failed-mutation-no-file-proof",
        family_id="V01",
        lane="sidecar",
        title="Failed file mutation mints no file-content proof",
        assertion=(
            "A failed edit/write invocation mints no file-content proof for the unapplied "
            "change; citing unapplied lines resolves as unverifiable."
        ),
        contract_refs=("V01",),
    ),
    GB03SlotSpec(
        selector="V01-11-web-scan-survey-surface-kinds",
        family_id="V01",
        lane="sidecar",
        title="Web, scanner, survey, and surface tools mint kind, URL, artifact, and trust",
        assertion=(
            "Admitted web#1 (untrusted + URL), scan#1, survey#1, render#1, page#1, and "
            "terminal_session#1 (with artifact_id) match their delivery log entries."
        ),
        contract_refs=("V01", "D1-evidence-kinds"),
    ),
    GB03SlotSpec(
        selector="V01-12-f2-dialed-host-contact-only",
        family_id="V01",
        lane="sidecar",
        title="F2 dialed_host# contact record alone grants no URL citation support",
        assertion=(
            "A stored mediated contact record dialed_host#1 (model_visible=False) "
            "never grants URL citation support for https://dialed.example.test and "
            "is never accepted as a handle citation."
        ),
        contract_refs=("V01",),
        finding_refs=("GB01-F2",),
    ),
    GB03SlotSpec(
        selector="V01-13-f2-retrieved-host-contact-only",
        family_id="V01",
        lane="sidecar",
        title="F2 retrieved_host# contact record alone grants no URL citation support",
        assertion=(
            "A stored mediated contact record retrieved_host#1 (model_visible=False) "
            "never grants URL citation support for https://retrieved.example.test."
        ),
        contract_refs=("V01",),
        finding_refs=("GB01-F2",),
    ),
    GB03SlotSpec(
        selector="V01-14-f2-audited-host-contact-inventory",
        family_id="V01",
        lane="sidecar",
        title="F2 full three-prefix contact inventory (dialed/retrieved/audited) isolation",
        assertion=(
            "Inventory all three mediated contact prefixes (dialed_host#, retrieved_host#, "
            "audited_host#) together: none is model-visible, none creates a citable observation "
            "or URL support on its own, while an admitted web#1 URL resolves as matched."
        ),
        contract_refs=("V01",),
        finding_refs=("GB01-F2",),
    ),
)

# ---------------------------------------------------------------------------
# V03 · Supersession and history (8 sidecar slots)
# ---------------------------------------------------------------------------
V03_SLOTS: tuple[GB03SlotSpec, ...] = (
    GB03SlotSpec(
        selector="V03-01-historical-handle-resolves-captured-body",
        family_id="V03",
        lane="sidecar",
        title="Superseded handle resolves its own captured historical body",
        assertion=(
            "After reading a.go (read#1), editing it, and reading again (read#2 superseding read#1), "
            "citing [read#1:2] with the original captured excerpt resolves to read#1."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-02-unqualified-path-resolves-current-only",
        family_id="V03",
        lane="sidecar",
        title="Unqualified path resolves only against current observation",
        assertion=(
            "After supersession, [a.go:2] with the current excerpt resolves to read#2, "
            "whereas [a.go:2] with the superseded excerpt is refused as unverifiable."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-03-historical-handle-newer-excerpt-refused",
        family_id="V03",
        lane="sidecar",
        title="Historical handle combined with newer observation excerpt is refused",
        assertion=(
            "Citing [read#1:2] with an excerpt that exists only in read#2 is refused; "
            "the host never silently substitutes the newer body."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-04-current-handle-older-excerpt-refused",
        family_id="V03",
        lane="sidecar",
        title="Current handle combined with superseded observation excerpt is refused",
        assertion=(
            "Citing [read#2:2] with an excerpt that existed only in superseded read#1 "
            "is refused as unverifiable."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-05-handle-contradictory-path-refused",
        family_id="V03",
        lane="sidecar",
        title="Handle combined with path from another observation is refused",
        assertion=(
            "Citing [read#1 internal/other.go:2] where read#1 observed internal/a.go "
            "is refused with reason wrong_path."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-06-command-receipt-survives-file-edit",
        family_id="V03",
        lane="sidecar",
        title="Command receipt remains valid after a mentioned file changes",
        assertion=(
            "command#1 mentioning internal/a.go stays valid and resolves as matched "
            "after internal/a.go is edited and superseded by read#2."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-07-git-and-merge-receipts-survive-file-edit",
        family_id="V03",
        lane="sidecar",
        title="Git and merge/overlay receipts remain valid after file edits",
        assertion=(
            "git_receipt#1 and overlay_receipt#1 remain valid and resolve as matched "
            "after subsequent edits supersede file-region observations of the mentioned paths."
        ),
        contract_refs=("V03",),
    ),
    GB03SlotSpec(
        selector="V03-08-trust-survives-supersession",
        family_id="V03",
        lane="sidecar",
        title="Trust recorded on an observation survives supersession",
        assertion=(
            "Trust=untrusted recorded on read#1 remains trust=untrusted after read#2 "
            "(trust=trusted) supersedes read#1; record comparison enforces trust preservation."
        ),
        contract_refs=("V03", "GB02-R2"),
    ),
)

# ---------------------------------------------------------------------------
# V04 · Command observations (10 sidecar slots, incl. Decision 005/014 slot 10)
# ---------------------------------------------------------------------------
V04_SLOTS: tuple[GB03SlotSpec, ...] = (
    GB03SlotSpec(
        selector="V04-01-command-handle-carried",
        family_id="V04",
        lane="sidecar",
        title="Command observation carries its command# handle",
        assertion=(
            "Every command execution result carries its command#1 handle and resolves "
            "[command#1] as matched."
        ),
        contract_refs=("V04",),
    ),
    GB03SlotSpec(
        selector="V04-02-f3-correctly-numbered-single-file",
        family_id="V04",
        lane="sidecar",
        title="F3 contrast (a): correctly numbered command source output is matched",
        assertion=(
            "Numbered source output (`cat -n internal/a.go`) mints a file-region record "
            "with line_numbered=True, printed_other_numbers=False; coordinate [read#1:2] is matched."
        ),
        contract_refs=("V04",),
        finding_refs=("GB01-F3",),
    ),
    GB03SlotSpec(
        selector="V04-03-f3-unnumbered-source-delivery",
        family_id="V04",
        lane="sidecar",
        title="F3 contrast (b): unnumbered command source delivery is traced",
        assertion=(
            "Unnumbered source output (`cat internal/a.go`) with line_numbered=False, "
            "printed_other_numbers=False resolves an in-range coordinate [read#1:2] as traced."
        ),
        contract_refs=("V04",),
        finding_refs=("GB01-F3",),
    ),
    GB03SlotSpec(
        selector="V04-04-multi-run-split-per-file-and-run",
        family_id="V04",
        lane="sidecar",
        title="Numbered command output makes one record per file and consecutive line run",
        assertion=(
            "Non-contiguous numbered slices across two files mint separate records per "
            "file and consecutive run; lines in the undelivered gap resolve as unverifiable."
        ),
        contract_refs=("V04",),
    ),
    GB03SlotSpec(
        selector="V04-05-sixteen-delivery-tag-cap",
        family_id="V04",
        lane="sidecar",
        title="Command numbered output caps host-issued delivery tags at 16",
        assertion=(
            "A command printing 20 distinct numbered files caps emitted source delivery tags "
            "at at most 16 while preserving command#1 as citable."
        ),
        contract_refs=("V04",),
    ),
    GB03SlotSpec(
        selector="V04-06-opaque-command-no-read-authority",
        family_id="V04",
        lane="sidecar",
        title="Opaque command printing a path creates no read authority",
        assertion=(
            "Printing a file path and line number via echo/printf creates no source "
            "observation or read authority; [internal/secret.go:10] is unverifiable while [command#1] is matched."
        ),
        contract_refs=("V04",),
    ),
    GB03SlotSpec(
        selector="V04-07-line-filter-pipeline-command-only",
        family_id="V04",
        lane="sidecar",
        title="Line filter in pipeline leaves only command# handle citable",
        assertion=(
            "Piping numbered source through a line filter (`cat -n internal/a.go | grep -v debug`) "
            "leaves only command#1 citable; file-coordinate citations resolve as unverifiable."
        ),
        contract_refs=("V04",),
    ),
    GB03SlotSpec(
        selector="V04-08-f3-mismatched-printed-numbers-no-excerpt",
        family_id="V04",
        lane="sidecar",
        title="F3 contrast (c): mismatched printed numbers without excerpt",
        assertion=(
            "In `cat -n internal/a.go internal/b.go`, the second file carries continuous printed "
            "numbers (4..6) vs true lines (1..3) with printed_other_numbers=True. An inferred "
            "unexcerpted coordinate [read#2:2] is refused under D1's product rule (unverifiable) "
            "while recording contract_reading_verdict=traced and disputed_expectation=True."
        ),
        contract_refs=("V04", "Decision-005"),
        finding_refs=("GB01-F3",),
        alias_selectors=("V04-displayed-continuous-numbers", "V04-rejected-inferred-coordinate"),
    ),
    GB03SlotSpec(
        selector="V04-09-f3-mismatched-printed-numbers-with-excerpt",
        family_id="V04",
        lane="sidecar",
        title="F3 contrast (d): mismatched printed numbers with verified excerpt",
        assertion=(
            "On the same `cat -n internal/a.go internal/b.go` second-file observation "
            "(printed_other_numbers=True), supplying a uniquely matching verified excerpt "
            "[read#2:2 \"func Beta() int\"] resolves as matched."
        ),
        contract_refs=("V04", "Decision-005"),
        finding_refs=("GB01-F3",),
        alias_selectors=("V04-uniquely-matching-excerpt",),
    ),
    GB03SlotSpec(
        selector="V04-10-copied-second-file-tag",
        family_id="V04",
        lane="sidecar",
        title="Decision 005/014: exact copied host delivery tag for second file in cat -n",
        assertion=(
            "Copy the exact authenticated, in-scope host-issued delivery tag "
            "`[read#2 internal/b.go:1-3]` for the second file in `cat -n internal/a.go internal/b.go`. "
            "Retain exact tag, actual bytes, true coverage, and disputed_expectation=True; "
            "treat a product refusal as a candidate false reject under the copied-tag contract."
        ),
        contract_refs=("V04", "Decision-005", "Decision-014"),
        finding_refs=("GB01-F3",),
        alias_selectors=("V04-copied-second-file-tag", "V04-copied-source-tag-second-file"),
    ),
)

# ---------------------------------------------------------------------------
# V05 · Compaction and recall (5 sidecar slots)
# ---------------------------------------------------------------------------
V05_SLOTS: tuple[GB03SlotSpec, ...] = (
    GB03SlotSpec(
        selector="V05-01-compaction-preserves-evidence-by-digest",
        family_id="V05",
        lane="sidecar",
        title="Compaction preserves lossless evidence records by SHA-256 digest",
        assertion=(
            "After transcript compaction removes original tool rows, every committed "
            "evidence record retains its exact body_sha256, delivered_ranges, and revision."
        ),
        contract_refs=("V05",),
    ),
    GB03SlotSpec(
        selector="V05-02-recall-by-handle-after-compaction",
        family_id="V05",
        lane="sidecar",
        title="Compacted evidence remains reachable via recall by handle",
        assertion=(
            "After compaction, read#1 is recallable (is_recallable=True) and resolves "
            "exact handle/line/excerpt citations identically to pre-compaction."
        ),
        contract_refs=("V05",),
    ),
    GB03SlotSpec(
        selector="V05-03-recalled-search-index-not-fresh-read",
        family_id="V05",
        lane="sidecar",
        title="Recalled search-index observation is not a fresh read of the file",
        assertion=(
            "A recalled grep#1 observation proves what the search index returned ([grep#1] matched) "
            "and never counts as a fresh read of uncovered lines in the file."
        ),
        contract_refs=("V05",),
    ),
    GB03SlotSpec(
        selector="V05-04-source-change-excluded-from-recall",
        family_id="V05",
        lane="sidecar",
        title="source_change# brief row is excluded from recall and line/excerpt proof",
        assertion=(
            "source_change#1 is unavailable to recall (is_recallable=False), creates no "
            "citation duty, and rejects line/excerpt citations."
        ),
        contract_refs=("V05", "COV-05"),
    ),
    GB03SlotSpec(
        selector="V05-05-d16-secret-canary-screened-before-commit",
        family_id="V05",
        lane="sidecar",
        title="D16 secret canary is screened before evidence ledger commit",
        assertion=(
            "A synthetic non-live secret canary in tool output is redacted/screened before "
            "commit so the raw canary bytes never appear in the committed evidence body."
        ),
        contract_refs=("V05", "D16"),
    ),
)

# ---------------------------------------------------------------------------
# V06 · Scope and provenance (10 sidecar slots)
# ---------------------------------------------------------------------------
V06_SLOTS: tuple[GB03SlotSpec, ...] = (
    GB03SlotSpec(
        selector="V06-01-coordinator-own-observations",
        family_id="V06",
        lane="sidecar",
        title="Coordinator cites its own first-hand observations",
        assertion=(
            "Coordinator resolves its own read#1 observation without leg prefix as matched."
        ),
        contract_refs=("V06",),
    ),
    GB03SlotSpec(
        selector="V06-02-coordinator-dispatched-worker-a-namespaced",
        family_id="V06",
        lane="sidecar",
        title="Coordinator cites dispatched worker-a observation with leg namespace",
        assertion=(
            "Coordinator resolves [worker-a:read#1:2] against dispatched child session "
            "worker-a as matched."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
    GB03SlotSpec(
        selector="V06-03-coordinator-two-workers-leg-namespaced-union",
        family_id="V06",
        lane="sidecar",
        title="Coordinator leg-namespaced union spans coordinator plus two dispatched workers",
        assertion=(
            "Coordinator permitted_union_handles includes own read#1 plus worker-a:read#1 "
            "and worker-b:read#1, resolving both dispatched legs cleanly."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
    GB03SlotSpec(
        selector="V06-04-worker-read-not-parent-first-hand",
        family_id="V06",
        lane="sidecar",
        title="Worker read is never parent coordinator's first-hand read",
        assertion=(
            "Coordinator citing worker-a's read#1 without the worker-a: leg namespace "
            "is refused as unverifiable (wrong_session)."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
    GB03SlotSpec(
        selector="V06-05-worker-cannot-cite-sibling-worker",
        family_id="V06",
        lane="sidecar",
        title="Worker A cannot cite sibling Worker B observations",
        assertion=(
            "Worker A citing [worker-b:read#1:2] is refused as unverifiable (wrong_leg)."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
    GB03SlotSpec(
        selector="V06-06-worker-cannot-borrow-parent-evidence",
        family_id="V06",
        lane="sidecar",
        title="Worker cannot borrow parent coordinator's read or URL evidence",
        assertion=(
            "Worker A citing parent coordinator's read#1 or parent's observed URL is "
            "refused as unverifiable."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
    GB03SlotSpec(
        selector="V06-07-second-chat-isolation",
        family_id="V06",
        lane="sidecar",
        title="Evidence from a second chat never resolves",
        assertion=(
            "Observations minted in a separate chat session (sess-chat-2) never resolve "
            "in sess-chat-1 (unverifiable, wrong_session)."
        ),
        contract_refs=("V06",),
    ),
    GB03SlotSpec(
        selector="V06-08-second-project-isolation",
        family_id="V06",
        lane="sidecar",
        title="Evidence from a second project never resolves",
        assertion=(
            "Observations minted in a separate project session (sess-proj-2) never resolve "
            "in sess-proj-1 (unverifiable, wrong_session)."
        ),
        contract_refs=("V06",),
    ),
    GB03SlotSpec(
        selector="V06-09-worker-round-boundary-cited-results",
        family_id="V06",
        lane="sidecar",
        title="Worker round boundary separates current source from cited_results",
        assertion=(
            "In worker round-2, round-1 read#1 is refused as prior_round_source_evidence "
            "by default and resolves only when explicitly permitted via cited_results_handles."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
    GB03SlotSpec(
        selector="V06-10-workspace-refresh-rejection-invalidation",
        family_id="V06",
        lane="sidecar",
        title="Workspace refresh or round rejection invalidates stale source observations",
        assertion=(
            "After workspace_refreshed=True or round-1 in invalidated_rounds, prior-round "
            "read#1 is refused as stale_round_invalidated even if listed in cited_results_handles."
        ),
        contract_refs=("V06", "GB02-R3"),
    ),
)

# ---------------------------------------------------------------------------
# V07 · Restart and an unreadable ledger (2 sidecar + 4 in-process slots)
# ---------------------------------------------------------------------------
V07_SLOTS: tuple[GB03SlotSpec, ...] = (
    GB03SlotSpec(
        selector="V07-01-sidecar-restart-after-commit-no-handle-reuse",
        family_id="V07",
        lane="sidecar",
        title="Sidecar restart after commit never reuses or renumbers handles",
        assertion=(
            "Restarting the sidecar after committing read#1 preserves read#1 intact and "
            "mints read#2 for the next read without handle reuse or renumbering."
        ),
        contract_refs=("V07",),
    ),
    GB03SlotSpec(
        selector="V07-02-sidecar-unreadable-ledger-no-exemption",
        family_id="V07",
        lane="sidecar",
        title="Sidecar unreadable ledger is never treated as empty and grants no exemption",
        assertion=(
            "When the evidence ledger is unreadable (readable=False), has_citable_observation "
            "returns None, exempt speech (evidence_optional=True) is rejected as false_accept, "
            "and citations resolve as unverifiable (unreadable_ledger)."
        ),
        contract_refs=("V07", "S01"),
    ),
    GB03SlotSpec(
        selector="V07-03-inprocess-restart-before-commit-boundary",
        family_id="V07",
        lane="in_process",
        title="In-process restart before commit boundary prevents phantom handles",
        assertion=(
            "Crash/restart before ledger commit leaves no phantom record; the next "
            "committed observation receives a clean monotonic handle without collision."
        ),
        contract_refs=("V07",),
    ),
    GB03SlotSpec(
        selector="V07-04-inprocess-restart-before-resolution-boundary",
        family_id="V07",
        lane="in_process",
        title="In-process restart between commit and resolution preserves exact verification",
        assertion=(
            "Restarting between commit and citation resolution reloads the committed "
            "record by identical handle and digest and resolves [read#1:2] as matched."
        ),
        contract_refs=("V07",),
    ),
    GB03SlotSpec(
        selector="V07-05-inprocess-unreadable-ledger-refuses-all-citations",
        family_id="V07",
        lane="in_process",
        title="In-process unreadable ledger refuses handle, path, URL, and artifact citations",
        assertion=(
            "With DeliveryLog(readable=False), every citation type (handle, path_coord, "
            "handle_path, url, artifact) resolves as unverifiable (unreadable_ledger)."
        ),
        contract_refs=("V07",),
    ),
    GB03SlotSpec(
        selector="V07-06-inprocess-unreadable-ledger-denies-exemption",
        family_id="V07",
        lane="in_process",
        title="In-process unreadable ledger denies exempt speech and invents no handles",
        assertion=(
            "With DeliveryLog(readable=False), check_slot classifies evidence_optional=True "
            "as false_accept and permitted_union_handles is empty."
        ),
        contract_refs=("V07", "S01"),
    ),
)

GB03_FIXED_SLOTS: tuple[GB03SlotSpec, ...] = (
    V01_SLOTS + V03_SLOTS + V04_SLOTS + V05_SLOTS + V06_SLOTS + V07_SLOTS
)

SLOTS_BY_SELECTOR: dict[str, GB03SlotSpec] = {
    slot.selector: slot for slot in GB03_FIXED_SLOTS
}


def slots_for_family(family_id: str) -> tuple[GB03SlotSpec, ...]:
    """Return all fixed slots belonging to `family_id` in manifest order."""
    return tuple(s for s in GB03_FIXED_SLOTS if s.family_id == family_id)


def gb03_manifest_counts() -> dict[str, dict[str, int]]:
    """Return per-family sidecar and in-process counts for all GB03 fixed families."""
    counts: dict[str, dict[str, int]] = {}
    for fam in GB03_FIXED_FAMILY_IDS:
        fam_slots = slots_for_family(fam)
        counts[fam] = {
            "sidecar": sum(1 for s in fam_slots if s.lane == "sidecar"),
            "in_process": sum(1 for s in fam_slots if s.lane == "in_process"),
            "total": len(fam_slots),
        }
    return counts


def validate_gb03_catalog() -> dict[str, int]:
    """Assert exact agreement with `ledger.md` and `grounding.selectors`."""
    if len(SLOTS_BY_SELECTOR) != len(GB03_FIXED_SLOTS):
        raise ValueError("Duplicate GB03 slot selector detected")

    ledger_fixed = {f.family_id: f for f in FIXED_FAMILIES if f.card == "GB03"}
    ledger_gen = {f.family_id: f for f in GENERATED_FAMILIES if f.card == "GB03"}

    counts = gb03_manifest_counts()
    for fam_id, fam_meta in ledger_fixed.items():
        actual = counts.get(fam_id)
        if actual is None:
            raise ValueError(f"Missing GB03 fixed family {fam_id!r} in catalog")
        if actual["sidecar"] != fam_meta.sidecar_count:
            raise ValueError(
                f"{fam_id} sidecar slot mismatch: catalog={actual['sidecar']}, "
                f"ledger={fam_meta.sidecar_count}"
            )
        if actual["in_process"] != fam_meta.in_process_count:
            raise ValueError(
                f"{fam_id} in_process slot mismatch: catalog={actual['in_process']}, "
                f"ledger={fam_meta.in_process_count}"
            )

    v02_meta = ledger_gen["V02"]
    if V02_GENERATED_SPEC.in_process_per_platform_per_seed != v02_meta.in_process_count:
        raise ValueError("V02 in_process count mismatch against ledger")
    if V02_GENERATED_SPEC.sidecar_per_platform_per_seed != v02_meta.sidecar_count:
        raise ValueError("V02 sidecar count mismatch against ledger")

    total_sidecar = sum(c["sidecar"] for c in counts.values())
    total_in_process = sum(c["in_process"] for c in counts.values())
    return {
        "fixed_sidecar_slots": total_sidecar,
        "fixed_in_process_slots": total_in_process,
        "fixed_total_slots": total_sidecar + total_in_process,
        "v02_in_process_per_seed": V02_GENERATED_SPEC.in_process_per_platform_per_seed,
        "v02_sidecar_per_seed": V02_GENERATED_SPEC.sidecar_per_platform_per_seed,
    }
