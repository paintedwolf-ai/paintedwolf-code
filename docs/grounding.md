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

File supersession never invalidates a command, Git receipt, artifact, or other event observation merely because its output mentions a changed file. A merge receipt still proves the recorded merge outcome after a file is edited. Native Git operations produce their own catalog-declared receipts, including commit identities, graph observations, and affected paths. Verification and overlay operations also publish citable outcome receipts through this pipeline. Those citations establish the recorded outcome; source-verification and promotion gates still evaluate their own structured facts. Historical evidence retains its original trust; supersession cannot erase prior exposure to untrusted content.

Portable program results receive `command#` observation handles alongside any
confirmed source-delivery handles. These observations retain the screened output
seen by the model, including incomplete and failed execution outcomes. Their
opaque command shape does not index printed paths, create source read authority,
prove visual observations, or supply workflow verification verdicts. Source and
verification evidence continue through their respective typed owners. The records
use the existing versioned evidence payload and command-handle meaning; retained
observations are unchanged.

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

Worker repair feedback describes the rejected handle's observation and names a
replacement source handle only when exactly one current-job file-region record
verifies the supplied path, line, and excerpt. This is a suggestion: the worker
must resubmit the corrected report, and the host never substitutes provenance
for an explicitly contradictory handle. Scanner-only findings cite the scanner
handle without source coordinates; source findings cite the source observation.
Web findings cite a web handle without source coordinates and put observed URLs
in `cited_urls`.

A resumed child keeps its conversation, but a new worker job has a new evidence
boundary. Earlier reads and fetches must be reacquired before the new job can
cite them. Filtering unsupported claims at retry exhaustion preserves useful
partial output; it does not prove the original assignment complete.

A bare path names a file, not a claim: useful for navigation, insufficient as line evidence. A bare handle is sufficient for non-line observations such as a directory listing, command result, or scan artifact, because the handle already identifies one record.

Resolution produces one of five verdicts (`internal/evidence/resolve.go`):

- **matched**: the supplied claim and observation agree directly;
- **traced**: the reference reaches the observation but the prose is a paraphrase or lower-resolution claim;
- **bound**: the exact resolver bounced the citation, but the host found exactly one ledger record satisfying every structured field the claim supplied, and adopted its canonical handle;
- **ambiguous**: more than one ledger record satisfies the claim;
- **unverifiable**: the host cannot connect the claim to admitted evidence.

Every file-reading tool retains its lines in the host's numbered-line grammar (`internal/hostmarker`), and the verifiers read that same grammar back from retained bodies. Model requests present those lines as unlabeled source blocks headed by their line range ([source text in model requests](agent-tool-feedback.md#source-text-in-model-requests)), so a worker cites a line by its position from the block's first line and copies the excerpt as source text. The meaningful-span floor applies to the excerpt as the worker cited it. A path citation whose line is wrong but whose excerpt matches exactly one live line of the cited path is re-anchored to that line and the correction is written back onto the citation and counted in the grounding audit; an excerpt that matches several lines, or none, is not corrected.

Matched, traced, and bound are grounded; ambiguous and unverifiable block the strict completion paths. Binding exists because a citation can be correct and still fail an exact match (a differently normalized path, a stale ordinal), and refusing it would spend repair budget on a claim the evidence already supports. Where several records fit, the host has no basis for choosing one, and picking would manufacture a provenance the worker never established; that is why `ambiguous` is a separate verdict rather than a tie broken quietly.

### Search coordination

Search indexes evidence and grounding outcomes as projections; it does not re-parse compacted messages to recreate evidence. Search results retain handle, path, excerpt, trust, verdict, and source identity. Following a hit may reacquire the current source, but the stored hit remains an observation of the earlier fact. Search and grounding share identity without sharing authority.

---

## Evidence-grounded prose

A coordinator's speech is Markdown, with citations inline where each claim is made. Citations make the underlying observations verifiable by the reader; they do not certify the answer.

| Form | Meaning |
|---|---|
| `[read#3]`, `[leg-a:read#3]`, `[task#1]` | An observed handle, optionally namespaced by worker leg |
| `[read#3:42]`, `[read#3:42-58]` | A line, or a fully observed inclusive range, in that observation |
| `[read#3:42 "if err != nil"]` | A line and excerpt, re-anchored within the existing bound |
| `[internal/a.go:42]`, `["docs/design notes.md":12]` | A workspace path and coordinate; quote a path with spaces |
| `[read#3:42, leg-a:grep#2:18]` | A group, decided as a group |
| `[RFC 9110](https://example.test/spec)`, `<https://example.test/spec>` | An HTTP(S) destination, which must have been observed |
| `[auth.go](internal/auth.go#L42)`, `[auth.go](source://root-id/internal/auth.go#L42)` | A workspace link; only a `#L` fragment is a coordinate |
| `[verify: targeted - reason]` | The verification assessment; a supporting receipt is a separate citation |
| `[artifact:<uuid>]` | A request to present a visual artifact; it does not stand in for a source citation |

A bracket counts as an attempt only on a structural signal, so ordinary prose stays prose. A coordinate on a path with `/` or `.` is an attempt; a path part of digits and dots is not, and a bracket directly after an identifier character is an index expression. A bare name such as `[Dockerfile]` or `[a/b.go]` is a citation only when the ledger knows that path, and `[pr#123]` is prose because `pr` is not a registered evidence kind. Near-misses such as `[a.go:42:10]` are malformed attempts that repair teaches. Standard links take precedence over bracket groups.

One structural parse ([`internal/citationmd`](../lycaon/internal/citationmd)) with source positions serves validation, repair, and presentation. Code fences, indented code, inline code, comments, and escaped brackets are literal, so a reply can explain citation syntax without citing anything, and a URL in inline code makes no claim. Inputs over a parser budget, including the 4,096-byte limit on one inline node, become structured issues; they are never truncated into acceptance. Presentation removes control directives and pathless handles, and rewrites resolved file references to navigable links. It never creates new Markdown structure in doing so.

Narrative text is advisory during evaluation. Citation nodes identify the references to resolve; prose mentioning a path outside a citation node never creates an observation or establishes the truth of a claim.

**What must be cited.** Two decisions stay separate. Every supplied reference is validated, on every turn. Whether citations are required depends on the current user-intent window: one with no citable tool or worker observations may omit them, even when an earlier window has evidence. A reply that cites nothing there is stored as exempt speech, `traced: true` with `evidence_optional: true`: nothing failed to trace, and no observed source stands behind it. A reader wanting observed-source success checks `traced && !evidence_optional`, and Den shows exempt speech with no evidence state. A ledger that cannot be read is not an empty ledger and never earns the exemption.

**Host attachment.** When the coordinator omits citations, the host preserves the answer and attaches a bounded set of recorded references immediately, without another model turn, from the existing worker-reference union and observed-source selection. Source selection may prioritize observed paths and URLs mentioned in the answer; unobserved references cannot enter the attached set, and an empty evidence ledger never produces invented sources. Without a model draft, the host may assemble a report directly from structured worker results. Den labels these references **Host-added (observed)** and explains that they were attached automatically. On the wire, `traced` records the citation audit and `host_assembled` identifies a host-assembled closeout; host-added references can remain `traced: false` and still be inspectable. Neither the badge nor the reference verdict certifies the answer.

**Citation repair.** When repair is needed, the host pins the narrative text of the first usable report, retains its valid citations, and asks only for corrected ones. Each retained occurrence has a stable identifier, and the refusal names the offending ones. The reply is one standalone directive per rejected occurrence, such as `[cite: c1 => read#5:42]` or `[cite: c2 => <https://example.test/observed-page>]`, or the same Markdown with only its citations changed. A repair applies whole or not at all: an unknown, stale, duplicated, or wrong-kind target, a directive with no repair open, or a change to the narrative is `CITATION_REPAIR_INVALID`, and the retained draft stands. Repeated-offender tracking keys on the offending values, not their identifiers. Repair choices come from the rejected citation's structured path or tool/kind identity; they name exact observations and any superseding file handle, as choices for the model, not inferred support for its narrative. If those fields identify no candidates, a bounded sample of current non-gate observations supplies choices without guessing the intended reference. Unobserved URLs always require repair. A missing handle never causes the host to repeat a commit, merge, or other effect; citation-only repair uses the observations already supplied. Selecting an operational tool batch retires the pinned body before admission or execution, including failed or rejected attempts. A validated terminal worker-card publication attempt retires the parent/root body before the transcript write, even when publication fails or its wake is deferred. A typed coordinator loop wake also retires the body before new observations are considered. Resumed work requires a fresh report; spent citation and document retry budgets remain spent. Document repair on a turn that offered tools likewise keeps them callable; only a repair on the final, prose-only turn forbids tool use, and its copy then names no tool. When bounded repair is exhausted, the host keeps the pinned report and the proposed references it can resolve, or attaches recorded sources through the same host-attachment path. Unresolved citation nodes leave the accepted prose, literal code stays, and an unobserved link keeps its label as plain text. Rejected references never become matched citations; the result keeps the rejection code and retry count, and Den exposes the host assembly and the individual reference checks.

**Reading the report.** A closeout is Markdown, read directly, with no envelope. A response that is entirely one `markdown` or `md` fence is unwrapped once, because otherwise every citation in it would be literal; no other fence unwraps. On a workflow's report phase only, a closed top-level `json` fence ending the document is the report document. Everywhere else, and for any other fence, a fence is a literal example that stays part of the answer. The report document is read strictly against its declared type, and every member that does not fit is named by path: undeclared, of the wrong type, the answer placed inside the fence, or a citation channel that now belongs inline in the Markdown above it. A report surface refuses such a draft as `REPORT_FENCE_UNREADABLE` before any other check. The refusal groups repeats and says when a member belongs elsewhere, as when `ask` sits inside each finding instead of once at the top level. A finding's `where` entries stay structured references, with an optional `end_line`, because they associate a finding with its source. A reply left with nothing to read once its nodes are set aside, or that is one bare JSON value, is `COORDINATOR_CLOSEOUT_NO_ANSWER`. JSON meant for the reader goes in a fence.

**Presentation repair.** Image syntax in the body presents nothing: the client shows captures only through `[artifact:<id>]`, and a remote image loads only on the reader's click. An answer that embeds a bare artifact id (`PRESENT_MARKDOWN_EMBED`) or an image source this chat never observed (`PRESENT_IMAGE_UNOBSERVED`) is refused before citation checks, on the parsed image nodes and raw HTML, so an example in code passes. This is the one refusal whose retry replaces the pinned narrative, because the body is what is wrong: the next answer stands whole, with its citations read fresh. It spends the citation retry limit and no grounding friction. When repair runs out, host assembly removes those embeds, binds bare artifact ids as attachments, and keeps images whose URLs were observed.

**Report document repair.** A report's fields (findings, set-asides, ask) are conclusions, not references, so a refusal of them, whether unreadable members or a run report's document rules, is repaired separately: the host keeps the pinned body, shows the whole current document fence, and asks for it back with what is missing added. Document repair has its own small budget (`max_report_document_retries`) and spends none of the citation friction. A citation repair and a document repair each state the attempt budget that actually applies to them. A document still refused when repair is exhausted is stored with every field the host read, never as accepted: its completion record carries every failing requirement as `defects`, unread members first, and the run fails as not accepted ([Workflows § Reports](workflows.md#reports)).

**Which evidence a closeout may cite.** A closeout may cite its own session's observations and those of every worker leg the session dispatched since the current user intent. The legs come from the dispatch records, not from the transcript, so compaction never narrows what a closeout may cite. Explicit workflow runs instead use run-owned dispatch records across phases; their required reviewers come from completed tasks in the review phase. A recovery continuation preserves ordinary implement intent and never cuts off earlier workflow evidence.

## Mid-turn notes

A coordinator speaks before the turn ends by writing text alongside its tool calls. On a surface that declares `publishes_mid_turn_text`, the host audits that text against the evidence observed before the response's tool batch runs, then publishes it as a standalone `agent_note` row or withholds it. The decision never stops the tools.

| Text | Outcome |
|---|---|
| Empty, including after parroted transport markers are stripped | No note and nothing owed |
| Valid references and required citations present, with something to display | Published |
| Uncited, with no citable observation yet in this intent window | Published as exempt speech |
| Uncited, after citable observations exist | Withheld, `MID_TURN_NOTE_UNGROUNDED` |
| A reference that does not resolve | Withheld, `MID_TURN_NOTE_HANDLE_UNGROUND` or `MID_TURN_NOTE_URL_ONLY_UNGROUND` |
| An unreadable citation node, or a response that is only JSON | Withheld, `MID_TURN_NOTE_INVALID_SYNTAX` |
| An unknown artifact, or an artifact embedded as an image | Withheld, `MID_TURN_NOTE_ARTIFACT_UNKNOWN` or `MID_TURN_NOTE_MARKDOWN_EMBED` |
| An image source this chat never observed | Withheld, `MID_TURN_NOTE_IMAGE_UNOBSERVED` |
| Evidence the host cannot read | Withheld, `MID_TURN_NOTE_EVIDENCE_UNREADABLE`; never treated as pre-evidence |

The audit uses only evidence already observed, so a first response cannot cite the `task#1` that its own pending call would create. A later one can, and a dispatch handle is recorded only for an accepted dispatch. `[task#1]` proves the dispatch, a survey handle the recorded survey, and `[command#4]` that command's execution and status; none proves more, and none is refused because an unrelated content record exists. Direct notes have no argument length limit beyond normal response limits and the inline node limit.

An unresolved handle or URL spends the shared grounding friction, so a session that burns it mid-turn reaches closeout exhaustion sooner. Missing citations and syntax errors charge nothing, because uncited narration and a malformed node make no evidence claim.

The note's identity derives from its carrier's message id, which makes publication idempotent and gives durable correlation with no extra column. Published text stays on the carrier in native position, exactly as the model wrote it, and is the model's one copy; the derived note row is what the user sees and is not projected to the model. A crash between carrier and note leaves the text unpublished, which is true, and nothing replays the tools. Stored notes and tool pairs from earlier sessions stay readable as they are. Feedback for withheld text and waiting is in [agent tool feedback](agent-tool-feedback.md#assistant-text-beside-tool-calls).

Text on other surfaces, in the report-writing synthesis surfaces, and in workers is private working context with nothing owed.

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
