# Host behavior

Coordinator and session behavior come from machine state and explicit actions, never from natural-language classifiers over user chat.

**See also:** [Agent contract](agent-contract.md) · [Agent tool feedback](agent-tool-feedback.md) · [Docs map](README.md)

**Machine truth:** structural guards in `lycaon/test/contract/architecture/user_text_behavior_closure_test.go` (walker in `user_text_behavior_closure_ast.go`) · host markers in `lycaon/internal/hostmarker/` and `lycaon/internal/guidance/host_markers.go` · session-shape helpers in `lycaon/internal/coordinator/surface/dispatch_hints.go`

---

## What the host branches on

| Signal class | Examples |
|--------------|---------|
| Transcript structure | `UserIntentBoundary`, `IsInternalTranscriptMessage`, tool-call / tool-result pairs |
| Tool identity | Which tool ran, structured args, reject `Code:` |
| Session ledger | Workers in flight, batch phase, overlay pending, progress checklist |
| Containment | Per-action `Contained` from `confine.DefaultConfinement`; boundary attribution on a failed run (`confine.FailureAttribution`: `subject` or none), which is what makes a stopped `verify` unverifiable rather than failed |
| Wire / config | OpenAPI fields, manifest gates, catalog YAML |
| Explicit human action | Den approve API, workflow start action, session create flags |

Working directory and environment are per-call `command`/`verify` args, not session state the host infers. Human approval is recorded only through explicit host actions.

`SessionUserTask` (first visible user message) and `SessionForwardedAttachments` (openable attachment locators) in `internal/coordinator/surface/dispatch_hints.go` are structural extraction, not intent classification.

Catalog workflows start only through explicit host actions. Coordinator `state_start` records a proposal and cannot authorize a start. Phase approval uses the ready approve API, which binds the run revision and Blueprint digest. No manifest field contains approval phrases.

---

## How it is enforced

The guard is structural, not a name list. `user_text_behavior_closure_ast.go` walks the coordinator and session packages, and the tests in [`user_text_behavior_closure_test.go`](../lycaon/test/contract/architecture/user_text_behavior_closure_test.go) fail on:

| Edge | Rule | Test |
|------|------|------|
| User-text parameter → behavior sink | A function taking `userPrompt` / `userMessage` / `message` / `text` must not reach `Emit` | `TestNoUserTextNLProbeToBehaviorSink` |
| `maybeQueue*Kick` on the prompt path | Every hook is registered with a trigger class, and no trigger class is NL intent | `TestPromptPathKickHooksRegistered` · `TestKickContractTriggerClassesNotUserText` |
| `strings.Contains(err.Error(), …)` | Typed errors and `errors.Is` instead of parsing error prose | `TestNoErrErrorStringParsingInCoordinatorPackages` |
| Coordinator prompt gates | Pongo gate vars are registered or banned; no ad-hoc boolean derived from user text | `TestCoordinatorPromptBooleanGatesRegistered` |
| Inform timing | No `Kick*` consts and no `"coordinator-"+id` dispatch | `TestNoKickConstOrCoordinatorConcatDispatch` ([`kick_constants_sync_contract_test.go`](../lycaon/test/contract/catalogs/kick_constants_sync_contract_test.go)) |

Because the scan matches the shape, a newly written classifier fails it under any name. A user message that discusses build policy (“we do not allow compatibility steps here”) must not queue a kick, set a prompt gate, or dispatch implementers; only an explicit coordinator tool call does that.

**Closed sinks:** `Emit` / `QueueDeferred` and the `CoordinatorPromptGates` merge. **Timing SSOT:** anchors and bindings in [`agent-prompt-template.md`](agent-prompt-template.md). To add an inform at an existing anchor, author a binding under `config/packs/painted-wolf/platform/host/bindings/` (or workflow `injects:`) plus a template under `config/packs/painted-wolf/platform/guidance/`.

## Tool-output routing

Tool-output compaction and routing branch on tool identity and chunk role, never on a substring of arbitrary tool-output content.

| Site | Pattern | Why it is allowed |
|------|---------|---------|
| `promptloop/batch.go` `stampDietFieldsOnCommit` | calls `tooloutput.IsOverlayPromoteTool(toolName)` at commit time and stamps `msg.DietStamp` / `msg.DietStampSource` | tool identity, checked once at commit |
| `compaction/chunk_compactor.go` `CompactChunk` | routes off the stamped `chunk.Class.Strategy` / `chunk.Class.Source`, never off `chunk.ToolName` at compact time | routes on the stamped diet class |
| `compaction/overlay_promote_chunk.go` `IsOverlayPromoteConflictProtected` | `strings.Contains(content, …)` on the `OVERLAY_PROMOTE_SPILL` / `BANNER_PROMOTE_CONFLICT_DIGEST` hint codes | reads back codes the host itself stamped; unreachable for other tools after the identity gate |
| `compaction/chunk_compactor.go` `strategyFor` | `strings.Contains(content, hostmarker.CompactionBannerOpen)` | idempotency for already-compacted chunks, using the named constant the compactor wrote |
| `compaction/overlay_promote_diet.go` | tool-name map plus read-path heuristics | diet by tool identity and read args, not output sniffing |

Content-substring routers are forbidden outside these sites. Guards: `TestNoContentSubstringToolRouting` in [`test/contract/tools/tool_output_routing_test.go`](../lycaon/test/contract/tools/tool_output_routing_test.go) blocks new content-sniff routers, and `TestCompactChunkRoutesOverlayPromoteByToolIdentity` in [`internal/llm/compaction/chunk_route_test.go`](../lycaon/internal/llm/compaction/chunk_route_test.go) keeps summarize output away from the promote preserver.

### Host markers

A needle that reads back a marker the host itself wrote is machine state, not prose, but only if it is one spelling. Two files hold markers as layers of one vocabulary:

| File | Holds | Why there |
|------|-------|-----------|
| [`internal/hostmarker/hostmarker.go`](../lycaon/internal/hostmarker/hostmarker.go) | Markers that cross a package boundary or reach the client: `CompactionBannerOpen` / `Close`, `VerbatimHeadTail`, `GuidanceBlockOpen`, `Rejected`, `CodeLine`, the evidence-handle pattern, and the overlay promote/reject event prefixes | Writer and reader sit on opposite sides of the import graph, and Den renders the same text. Stdlib-only, so any layer may import it; `./task codegen:host-markers` projects the `markers` slice into [`lycaon-den/src/chat/host-markers.generated.ts`](../lycaon-den/src/chat/host-markers.generated.ts) |
| [`internal/guidance/host_markers.go`](../lycaon/internal/guidance/host_markers.go) | Gate-layer vocabulary that stays inside the guidance plane: the block headers (`>>> Tool feedback`, `>>> Spec posture`, `>>> REQUIRED NEXT`), the seam markers a producer writes and the enricher reads back (`MarkerSecretReceipt`, `MarkerPeriodHint`), and `ContextTrimNotice` | One writer and one reader, both above `internal/guidance`; none reaches Den |

`host_markers.go` imports `hostmarker` and composes its headers from `GuidanceBlockOpen`, `Rejected`, and `CodeLine`, so a marker is spelled once. A new marker goes in `guidance` while it stays inside the gate layer and moves down to `hostmarker` the moment a second package or Den needs to read it. A constant absent from the `hostmarker.markers` slice exists in Go but never reaches Den.

## External protocol prose

An adapter may classify prose only when the external protocol supplies no typed equivalent, and only to choose a conservative protocol fallback or operator diagnostic: provider feature-negotiation errors (`response_format`, reasoning controls), Bedrock/egress retry categories, and OS credential-store messages. The adapter translates that input immediately into a typed internal outcome; coordinator, workflow, permission, and evidence floors never branch on the original text. When an upstream status or code exists, it takes precedence over prose.
