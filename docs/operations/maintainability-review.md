# Maintainability architecture review

This review covers the 40 automated maintainability findings open on October 11,
2026, plus the earlier API and tool architecture issues #342 and #352. Its source
baseline is `f9991b89ce895dc20f77aa80611c2e2217518cdb`. Delivery PRs below contain
the resulting implementation; GitHub records their current validation and merge
status. Measurements in issue prose are historical, not the current design input.

## Recommendation

Organize code around complete operations, coherent state lifetimes, and explicit
construction. Keep small collaborators subordinate to the boundary that settles
an operation. A type's field count does not distinguish a runtime service from
a construction record or an externally meaningful fact projection. A directory's
file count does not distinguish fragmentation from a useful integration matrix.
These measures identify review candidates; they cannot decide the architecture.

The current architecture already separates many of the domains described in
older issues. Preserve those gains. The worthwhile new boundaries are mounted
workspace surfaces, one connection binding per window, reader-directed transcript
paging, repository metadata observation, tool-result preparation, and verification
planning/execution/reporting. Keep HTTP middleware ordering, durable transitions,
source-store reclamation, confinement, and recovery contracts explicit.

The term subsystem owner retains its meaning from the
[architecture](../architecture.md): the Go boundary that authoritatively settles
an invoked operation. A presentation component, repository adapter, dependency
record, or extracted algorithm does not become a new subsystem owner.

## Design decisions by concern

| Concern and original issues | Recommended organization | Why |
| --- | --- | --- |
| Workspace composition: #463, #468 | Shell composes mounted chat, stage, sidebar, overlays and project dialogs; feature modules provide their narrow integration surfaces. | Rendering lifetimes and feature dependencies become visible without a broad controller or import barrel. |
| Layout and resize: #466, #467 | Keep persisted layout decisions distinct from mounted element measurement; retain a cohesive layout contract when its breadth is justified. | A split host measures its own element and cleans up observation with that element. |
| Connection: #390 | One immutable binding per window, explicit teardown, and singleflight discovery/verification. | Eight independently mutable registrations allow partial rebinding and late work against stale stores. |
| Transcript motion: #389 | Retain the shared motion coordinator; separate paging intent from resident geometry, fence responses by view generation, and bound retry and cache retention. | Page eviction must not reverse reader intent or cause repeated older/newer requests; transient failure must not permanently disable an edge. |
| Native shell directory: #528 | Review by actual native subsystem cohesion, retaining platform composition where appropriate. | Directory size alone does not justify another runtime boundary. |
| HTTP composition: #407, #408, #507, #512 | Retain route-family handlers, the ordered Server transport boundary, and explicit construction records. | Authentication, rate limits, person actions, restore fencing and lifecycle have one ordered HTTP path. Nested dependency bags would conceal the graph. |
| Worker and workflow: #514, #522, #526, #527, #510, #501, #394, #505, #515 | Retain fenced SQL transitions and their outbox in one adapter, ordered rewind dependencies, lifecycle admission, explicit persisted ask records and their integration tests. | Consumer interfaces already narrow access. Independent repositories or callback lists would spread transaction and teardown invariants. |
| Source lifecycle: #525, #517, #511, #509 | Retain the coordinated tree-store lifecycle, mutable directory overlay, repository analysis lifetime and immutable review projection. | Drain, writer exclusion, pins and reclamation share one locking protocol. Spill and revision-cached freeze represent one collection. |
| Source observation and recording: #516, #524 | Separate Git reference observation from descriptor coverage; prepare source changes beside the existing preparation contract. | Ref digests/debounce have their own state; descriptor accounting remains shared. Attribution and mutation still settle together. |
| Tool result construction: #518, #506, #521 | Separate image resolution, screening/rendering, source reading/budgeting, and post-tool feedback from construction-only dependencies. | Each step has a real input/output contract; runtime feedback need not retain the whole tool construction record. |
| Tool boundaries and evidence: #393, #519, #504, #508, #513 | Retain the single source write door, ordered approval protocol, explicit grounding projection and related tests. | Reducing counts must not expose private effect machinery or obscure declared capability behavior. |
| Backup and processes: #464, #391 | Retain staged restore/recovery and coordinated process launch/cleanup. | Their shared preimage and process lifetimes are the design constraint. |
| Compiler and confinement tests: #523, #520 | Keep the compiler semantics matrix and platform capability contracts discoverable together. | Moving white-box fixtures for a count target can weaken discoverability and broaden production visibility. |
| Verification: #469, #465 | Separate hosted planning, execution and evidence reporting; organize test discovery by those responsibilities and distribution/scheduler contracts. | The command surface should dispatch to cohesive modules, and tests should reveal which contract failed. |

Retained warnings have per-artifact rationales, ceilings and selected contract
fingerprints under [maintainability reviews](../../lycaon/test/contract/maintainability-reviews/).
Those records are the precise, current decisions. They remain revisable by agents;
this report is not a mandatory decomposition plan.

## A maintainability system for repeated agent use

The previous loop coupled a numeric warning to a cleanup issue. Repeated agents
could interpret that as a standing instruction to split files or types, even when
an earlier extraction had already established the right boundaries. Historical
issue prose could also outlive the architecture it described. Closing a warning
as not planned could suppress later changes indefinitely.

The revised loop separates measurement, admission, design and verification:

1. Measure the current artifact and expose its source identity, classification,
   workstream and previous observation. Preserve hard limits.
2. Let the agent choose a substantive refactor, consolidation, or bounded retention.
   Existing decisions inform that choice without prohibiting a better design.
3. Record a retained warning with a rationale, measured ceiling, relevant type
   shape and selected behavioral contracts. Construction records and data
   projections receive their own classifications.
4. Reopen review when growth exceeds the ceiling, a selected contract changes,
   a Go declaration/signature/dependency changes, or the decision is removed.
   Renaming an artifact does not inherit acceptance. Invalidated decisions create
   an issue even when no issue existed during the retained state.
5. Reconcile from verified main evidence. Preserve human issue text, stable issue
   identity and idempotence. A bounded retention closes the review obligation;
   it does not claim the artifact became numerically small.

Agents must not automatically raise ceilings or refresh fingerprints to silence
feedback. They can make those changes after reconsidering the design and verifying
its contracts. Decision changes appear in the budget report for review. This is
an engineering decision, not an additional approval ceremony.

A fingerprint detects changed text or declarations, not semantic correctness.
The recorded commit is provenance supplied by the review author. Passing
verification still comes from receipts and CI on the admitted source. Selecting
focused behavioral contracts avoids reopening a sound design for every comment
or unrelated implementation edit; conservative dependency fingerprints may renew
review when import structure changes.

Regression coverage includes retained-to-invalidated transitions, no-issue
retention followed by boundary-only changes, repeated reconciliation, changed
measurements, manual dismissal, renamed/deleted artifacts, hostile issue prose,
and type/dependency changes that do not increase field or method counts.

## Delivery and verification

| Delivery | Scope |
| --- | --- |
| [#530](https://github.com/paintedwolf-ai/paintedwolf-code/pull/530) | Architectural review policy, evidence, issue lifecycle and this report. |
| [#532](https://github.com/paintedwolf-ai/paintedwolf-code/pull/532) | Transcript paging, reader intent and bounded restoration/cache behavior. |
| [#533](https://github.com/paintedwolf-ai/paintedwolf-code/pull/533) | Workspace composition and mounted split measurement. |
| [#534](https://github.com/paintedwolf-ai/paintedwolf-code/pull/534) | Source observation and preparation boundaries. |
| [#535](https://github.com/paintedwolf-ai/paintedwolf-code/pull/535) | Tool-result preparation and feedback. |
| [#536](https://github.com/paintedwolf-ai/paintedwolf-code/pull/536) | Connection binding and lifecycle. |
| [#537](https://github.com/paintedwolf-ai/paintedwolf-code/pull/537) | Hosted verification responsibilities and test organization. |

Each implementation is delivered independently through the merge queue. Scoped
local checks qualify only their captured source and requested contracts. Ready
PR checks and merge-group integration qualify the actual landing sequence;
main qualification remains separate. Transcript unit/integration checks do not
claim physical WebKit qualification of unchanged native scrolling algorithms.

The older API/tool concerns #342 and #352 were checked against current source,
not just the merge reference for [#471](https://github.com/paintedwolf-ai/paintedwolf-code/pull/471).
`sourceapi.Handler` now has 13 named collaborators and no receiver methods;
`toolhost.Runtime` has nine collaborators and no receiver methods. The survey
gatherer has five methods/eight fields, and `toolexecution.Executor` has ten
methods/nine fields, with invocation settlement retained in `Invoke`.
`GuardContext` contains 21 typed occurrence domains and local evaluation state.

The API ticket's old requirement of fewer than 50 production files is not met
by `sourceapi` (52) or the API root (50). The current hard limit is 75. The
recommended disposition intentionally supersedes that old numeric prescription:
keep the package-private source-view/cache/revision protocol and HTTP composition,
with bounded review records and focused contracts. The tool ticket's listed
monoliths now fall below its old limits; additional current warnings are retained
explicitly where they describe cohesive contracts. These ten supplemental review
records are separate from the original 40-finding inventory.

The dependency-search tests exercise default versus explicit dependencies through
HTTP code/declaration search and source definition. Source-view tests preserve
access and revision fences, while watch coverage distinguishes policy-unwatched
paths from truncation. Tool tests preserve cancellation, exact settlement, scratch
authority and direct-IP lease settlement. OAR catalog, differential and runtime
isolation tests qualify the typed projection.
