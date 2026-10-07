# Grounding

Grounding binds agent claims and workflow outcomes to observations the host can resolve after the model turn, transcript compaction, worker completion, or process restart.

**See also:** [Architecture](architecture.md#grounding-and-host-authority) · [Coordination](coordination.md) · [Tools](tools.md) · [Worker result contract](worker-result-contract.md) · [Search](search.md) · [Prompt assembly](prompt-assembly.md) · [Scan findings](scan-findings.md) · [Visual surface](visual-surface.md)

**Machine truth:** enforcement policy in [`security/host/grounding.yaml`](../lycaon/config/packs/painted-wolf/security/host/grounding.yaml) · evidence-kind catalogs under `lycaon/config/` · records, handles, and verdicts in `lycaon/internal/evidence/` · circuit breaker in `lycaon/internal/grounding/` · closeout audit in `lycaon/internal/guidance/` and `lycaon/internal/delegation/`

---

## Why grounding is a host concern

Model prose is useful communication but weak authority. It can omit context, confuse a stale observation with a current one, or describe work performed by another session as if it were first-hand.

The host therefore records evidence at tool and subsystem-owner boundaries, then evaluates references to that evidence when a worker completes, a reviewer submits a verdict, a coordinator sends a mid-turn note, or a batch closes.

Grounding does not decide whether an action was permitted or whether a workflow gate exists. It answers a narrower question: does this claim resolve to the evidence and proof scope it cites?

## Evidence model

Evidence-producing tool results mint stable records and handles. An invoked command or Git effect that returns an error can also mint an outcome receipt; that receipt records the failure and never establishes successful execution. Refused invocations and failed file mutations do not mint file-content proof. A record carries what later resolution needs: kind, shape, path or URL, source ranges, body or digest, trust, source revision, and any visual or process binding.

| Concept | Rule |
|---------|------|
| Handle | Stable append-only identity `kind#ordinal` (ordinal is 1-based per kind) |
| Kind | Open semantic category declared by the producing tool/catalog |
| Shape | Closed resolution behavior such as file region, URL, command, artifact, or opaque observation |
| Path index | Normalized project coordinates to the handles that observed them |
| Provenance | Session, worker leg, tool receipt, source generation, and trust carried with the observation |

Evidence records are durable facts. Prompt compaction may remove the original tool row from the model's context, but it does not paraphrase or delete the evidence row, and the row stays addressable through `recall` by its handle ([Tools § Recall](tools.md#recall)). A recalled search-index observation is evidence of what the index returned, not a fresh read of the file; currency and provenance remain visible so the caller can decide whether to reacquire the source.

Observation identity and file currency are separate. Exact handles keep resolving their captured bodies after another read or mutation supersedes them. The path index omits superseded file observations when resolving an unqualified path; an explicit handle can still cite the earlier snapshot. Combining an explicit handle with a path or excerpt must agree with that same observation; the host never substitutes a newer file body to make a contradictory historical citation pass.

File supersession never invalidates a command, Git receipt, artifact, or other event observation merely because its output mentions a changed file. A merge receipt still proves the recorded merge outcome after a file is edited. Native Git operations produce their own catalog-declared receipts, including commit identities, graph observations, and affected paths. Historical evidence retains its original trust; supersession cannot erase prior exposure to untrusted content.

### Transcript diet vs evidence ledger

The transcript is presentation history; the evidence ledger is the resolution substrate. They are linked but have different retention and compaction behavior.

Tool JSON is parsed only at declared envelope boundaries. Braces inside ordinary output do not establish structured evidence metadata.

Tool output is screened for secrets before either surface commits. After that boundary, evidence remains lossless under its retention policy while model-facing history may be summarized, spilled, or omitted to fit a context window. Prompt optimization therefore cannot weaken auditability, and a later parser cannot treat decorated display text as the original observation.

### Scan evidence shape

Native scan tools produce artifact-shaped evidence. The scan result, finding fingerprints, normalized locations, and engine identity are resolvable from the structured result rather than from a prose summary. Exploring scan results does not itself satisfy a workflow security verdict; a reviewer or gate must still cite the scan evidence and submit the declared outcome.

---

## Proof layers

Several proof systems compose around the evidence ledger:

| Layer | Proves |
|-------|--------|
| Tool receipt | A defined subsystem-owner invocation ran and settled with a stated outcome |
| Delegation ledger | A worker leg was dispatched and reached a structured disposition |
| Source verification | A check passed against the current source generation |
| Citation evaluation | A claim resolves to observed evidence in the permitted scope |
| Workflow gate | The manifest's declared obligation is satisfied |
| Authorization ledger | Authority was granted, denied, or exercised under a sealed context |

No layer substitutes for another. A passing test does not prove a human approved the command; an approval does not prove the test passed; a citation does not invent a workflow gate.

### Delegation ledger

Coordinator intent becomes proof only when worker jobs, leg criteria, and completion envelopes exist. The host evaluates dispatch and completion from those records, not from the coordinator saying that it delegated or that every worker succeeded. Ambient `implement@` uses the same worker queue and evidence model even without an explicit delegation; the accounting shape differs, the proof requirement does not.

### Proof scope and execution isolation

Evidence belongs to the session and execution boundary that observed it. Parent synthesis may union child ledgers, but the union keeps the leg namespace. A worker's file read is child evidence; it does not become a parent first-hand read.

Test evidence is content-bound. A receipt identifies the admitted source snapshot captured before execution, not the watcher epoch at completion. Unchanged content remains current despite watcher churn; changed content does not inherit the pass. This freshness applies to required test gates and evidence display, not to permission to finish routine work. Validation scope is an agent judgment, reported separately from host-observed execution outcomes.

Worker overlay verification applies to the worker snapshot. Promotion can create a new integrated generation, so the landed result may require combined verification even when every child was green in isolation.

---

## Verifiable citations

A citation must name enough structure for the host to resolve the observation:

| Citation | Resolution |
|----------|------------|
| Evidence handle | Resolves the exact observation if it belongs to the allowed evidence union |
| Path + line/excerpt | Resolves against observed file-region evidence |
| Handle + path | Resolves when both name the same observation; a handle that contradicts the path is refused |
| URL | Resolves only when the URL was observed through an admitted tool path |
| Artifact id | Resolves to a present, permitted visual or report artifact |

A bare path names a file, not a claim: useful for navigation, insufficient as line evidence. A bare handle is sufficient for non-line observations such as a directory listing, command result, or scan artifact, because the handle already identifies one record.

Resolution produces one of five verdicts (`internal/evidence/resolve.go`):

- **matched**: the supplied claim and observation agree directly;
- **traced**: the reference reaches the observation but the prose is a paraphrase or lower-resolution claim;
- **bound**: the exact resolver bounced the citation, but the host found exactly one ledger record satisfying every structured field the claim supplied, and adopted its canonical handle;
- **ambiguous**: more than one ledger record satisfies the claim;
- **unverifiable**: the host cannot connect the claim to admitted evidence.

Every file-reading tool renders a line as the host's numbered-line grammar (`internal/hostmarker`), and the verifiers read that same grammar back from retained bodies, so an excerpt copied from a read compares against the line's text rather than its rendered number. The meaningful-span floor applies to the excerpt as the worker cited it. A path citation whose line is wrong but whose excerpt matches exactly one live line of the cited path is re-anchored to that line and the correction is written back onto the citation and counted in the grounding audit; an excerpt that matches several lines, or none, is not corrected.

Matched, traced, and bound are grounded; ambiguous and unverifiable block the strict completion paths. Binding exists because a citation can be correct and still fail an exact match (a differently normalized path, a stale ordinal), and refusing it would spend repair budget on a claim the evidence already supports. Where several records fit, the host has no basis for choosing one, and picking would manufacture a provenance the worker never established; that is why `ambiguous` is a separate verdict rather than a tie broken quietly.

### Search coordination

Search indexes evidence and grounding outcomes as projections; it does not re-parse compacted messages to recreate evidence. Search results retain handle, path, excerpt, trust, verdict, and source identity. Following a hit may reacquire the current source, but the stored hit remains an observation of the earlier fact. Search and grounding share identity without sharing authority.

---

## Evidence-grounded prose

Coordinator closeout is human-readable Markdown plus an optional bounded structured citation trailer. The host resolves supplied references and stores one canonical closeout envelope. Citations make the underlying observations verifiable by the reader; they do not certify the answer.

Narrative text is advisory during evaluation. Typed citation fields identify the references to resolve; prose mentioning paths or handles never creates an observation or establishes the truth of a claim.

**Host attachment.** When the coordinator omits citation metadata, the host preserves the answer and attaches a bounded set of recorded references immediately, without another model turn, from the existing worker-reference union and observed-source selection. Source selection may prioritize observed paths and URLs mentioned in the answer; unobserved references cannot enter the attached set, and an empty evidence ledger never produces invented sources. Without a model draft, the host may assemble a report directly from structured worker results. Den labels these references **Host-added (observed)** and explains that they were attached automatically. On the wire, `traced` records the citation audit and `host_assembled` identifies a host-assembled closeout; host-added references can remain `traced: false` and still be inspectable. Neither the badge nor the reference verdict certifies the answer.

**Citation repair.** When repair is needed, the host pins the first usable report body and metadata, preserves accepted references, and asks only for corrected citations. Repair choices come from the rejected citation's structured path or tool/kind identity; they name exact observations and any superseding file handle, as choices for the model, not inferred support for its narrative. Unobserved URLs always require repair. A missing handle never causes the host to repeat a commit, merge, or other effect; the model retrieves a recorded result through `recall`. When bounded repair is exhausted, the host keeps the pinned report and the proposed references it can resolve, or attaches recorded sources through the same host-attachment path. Rejected references never become matched citations; the result keeps the rejection code and retry count, and Den exposes the host assembly and the individual reference checks.

**Reading the report.** A closeout is Markdown ending in one report fence, a JSON envelope, or, during a repair, the fence alone for the pinned body. The report fence is a declared grammar, not a reading of prose: the last block of the draft is a CommonMark fenced code block tagged `json` or untagged, and its payload is a JSON object with at least one report member. Any other final block, such as an untagged code example without report members, stays part of the answer. Fences inside earlier code blocks never open or close one. The closeout prompts show a literal fence, and a contract test holds each example to this reader. Every form is read member by member against the report's declared type. Members that fit are kept, and every other one is named by path: undeclared, of the wrong type, or the answer placed inside the fence. A report surface refuses such a draft as `REPORT_FENCE_UNREADABLE` before any other check. The refusal groups repeats and says when a member belongs elsewhere, as when `ask` sits inside each finding instead of once at the top level. Only a draft with no Markdown answer at all is `COORDINATOR_CLOSEOUT_ENVELOPE_ONLY`.

**Report document repair.** A report's fields (findings, set-asides, ask) are conclusions, not references, so a refusal of them, whether unreadable members or a run report's document rules, is repaired separately: the host keeps the pinned body, shows the whole current document fence, and asks for it back with what is missing added. Document repair has its own small budget (`max_report_document_retries`) and spends none of the citation friction. A citation repair and a document repair each state the attempt budget that actually applies to them. A document still refused when repair is exhausted is stored with every field the host read, never as accepted: its completion record carries every failing requirement as `defects`, unread members first, and the run fails as not accepted ([Workflows § Reports](workflows.md#reports)).

**Which evidence a closeout may cite.** A closeout may cite its own session's observations and those of every worker leg the session dispatched since the current user intent. The legs come from the dispatch records, not from the transcript, so compaction never narrows what a closeout may cite. Explicit workflow runs instead use run-owned dispatch records across phases; their required reviewers come from completed tasks in the review phase. A recovery continuation preserves ordinary implement intent and never cuts off earlier workflow evidence.

## Mid-turn notes

A coordinator can communicate before the turn ends through a grounded note. The note is a tool effect, not an assistant closeout, and carries explicit citations under a stricter bar than final synthesis:

- at least one resolvable in-session citation;
- no unobserved URL or survey-only path standing in for a direct claim;
- no host-selected unrelated evidence;
- visual attachments limited to artifacts produced for the current intent.

Workers do not write to the human-facing transcript. They report through completion envelopes; the coordinator decides what belongs in the root conversation.

---

## Surface verification

Visual proof separates design intent from observed application state:

| Evidence | What it can support |
|----------|---------------------|
| Rendered mockup | Intended layout or design direction |
| Live page/surface snapshot | What the running application rendered and exposed semantically |
| Sealed command terminal capture | The settled terminal screen from one exact non-interactive process, including its execution boundary |
| Held terminal snapshot | A settled interactive terminal state after the recorded input sequence |
| Geometry measurement | Quantitative size, position, alignment, and viewport claims |
| Console/process log | Runtime diagnostics tied to the captured surface |

A mockup cannot prove that the running app behaves correctly. Flat command output cannot prove what a terminal screen rendered; `command` becomes terminal surface evidence only when invoked with its sealed `terminal_capture`. A screenshot cannot prove a pixel measurement the host never measured. The evidence shape must match the claim.

Visual artifacts in a final report stay referenced by stable id. Missing or unavailable bytes are represented explicitly so a report cannot imply an unavailable image was reviewed. A visual tool result names its image by the artifact id the host stamps on it; the citation handle is the one the evidence ledger mints for the result, never a name the tool chose. Details: [Visual surface](visual-surface.md).

---

## Synthesis wrapup gates

Coordinator synthesis becomes eligible when the batch has no unresolved execution obligations:

- worker jobs have terminal dispositions;
- pending write overlays are promoted, rejected, or preserved as an explicit blocker;
- progress reflects the terminal worker results;
- required verification is passed at the current integrated generation;
- workflow review and human gates are settled;
- cited evidence is available in the permitted union.

These are machine facts; the host does not search prose for "done," "blocked," or "tests pass." A partial or blocked result is a legitimate closeout when the workflow permits it and the blocker is stated from structured state. Grounding requires honesty, not forced success.

## Grounding friction

Citation repair is bounded. Rejects identify the offending handles, paths, or URLs and provide a sample of admissible evidence so the next attempt can fix the exact defect. Repeated failures spend a shared friction budget; the host eventually falls back to ledger assembly or a partial result rather than an unbounded formatting loop. The budget covers one user intent; in a workflow run each newly entered phase starts it afresh, while a phase re-entering itself continues it, so a long run does not spend its report's repairs on an earlier phase's citations.

How hard a failure lands is per-surface policy in `grounding.yaml`, and the difference is user-visible: an ungrounded coordinator post-turn **warns** (`post_turn.mode`), while a closeout (`closeout.mode`) and a `record_finding` write (`write.mode`) **block**. Writing a finding is held to the stricter bar because a finding becomes durable shared state that later work reads as established.

Above that sits a circuit breaker on repeated ungrounded turns, counting both the session total (`max_ungrounded_warnings`, shipped 3) and the consecutive run (`max_consecutive_ungrounded`, shipped 2). `escalate_mode: flag`, the shipped setting, raises an advisory; `block` stops the session at the threshold and returns `Code: COORDINATOR_GROUNDING_ESCALATED`. A session that reaches the escalated state stays escalated, so a model that has stopped citing evidence cannot spend an entire budget failing the same check.

Friction is lowest when evidence is collected and cited in the same scope that reports it: workers return grounded envelopes, and coordinators preserve those handles instead of paraphrasing away the proof.

## Invariants

- Evidence is recorded at subsystem-owner boundaries, never reconstructed from decorated result prose.
- Transcript compaction does not rewrite committed evidence, or narrow the worker legs a closeout may cite.
- Worker evidence retains worker provenance when used by the parent.
- Verification is valid only for the source generation it checked.
- Citation shape must match claim shape.
- Search is a projection of evidence and outcomes, not an alternate evidence store.
- Closeout repair is bounded and may fall back to conservative ledger assembly; a fallback never records a refused run report as accepted.
