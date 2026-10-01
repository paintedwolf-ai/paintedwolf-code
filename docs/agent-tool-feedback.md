# Agent tool feedback

When the host rejects, redirects, or completes an agent action, it returns structured facts plus catalog-rendered guidance. The agent branches on the code; prose explains the reason and the reachable remedy.

**See also:** [Agent contract](agent-contract.md) · [Open Agent Rules](open-agent-rules.md) · [Agent prompt templates](agent-prompt-template.md) · [Tools](tools.md) · [Coordination](coordination.md)

**Machine truth:** [anchor catalog](../lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml) · pack `policy/<CODE>.yaml` units → [`schemas/guidance_registry.json`](../schemas/guidance_registry.json) · [`tool-results.yaml`](openapi/components/schemas/session/tool-results.yaml) · reject templates under [`platform/guidance/reject/`](../lycaon/config/packs/painted-wolf/platform/guidance/reject)

## Why feedback is structured

A prose-only failure makes the model guess whether two sentences mean the same condition. A code-only failure is hard to act on. The contract keeps identity and explanation separate:

```text
Code + typed details + rendered explanation + reachable remedy
```

Host code chooses the code and facts. Catalog templates choose the sentence. Neither parses the other back apart. Tool output and metadata remain data; only the host tool procedures that accompany loaded native schemas carry instruction authority. A rejected human checkpoint is always projected as a structured decision plus the host denial frame, even when the person supplied no free-text guidance.

At rendering, [`oarcopy.FactsFromData`](../lycaon/internal/oarcopy/facts.go) publishes host facts under `paintedwolf.*` and leaves standard profile names bare. Generic `reason` and `field` details publish as `paintedwolf.rejection_reason` and `paintedwolf.rejection_field`. The `arg_validation_*` facts describe real argument checks; validators mark those with `RejectInvalidArguments`, and operational or policy failures never populate them.

On the wire, a tool result carries ordered `feedback` entries (`code`, typed `details`, optional `subject`), a producer-stated `dispatch` record for worker creation, and a producer-stated `completion` record for lifecycle work. `codes` is the compact ordered card-selection projection of `feedback`; it is never the only rejection data.

## Feedback channels

Lifecycle anchors determine when feedback is emitted. One anchor catalog drives immediate guards, queued guidance, completion banners, and workflow notices: host code emits an anchor envelope, bindings select the template. A new sentence in Go or an ad hoc dispatch id would create a second timing system and is forbidden.

| Channel | When | Delivery |
|---|---|---|
| Immediate rejection | the requested host transition cannot proceed | tool result with stable code and typed details |
| Next-turn nudge | the completed coordinator turn violated a recoverable behavioral rule | structured user-role guidance before the next eligible prompt |
| Informational anchor | a lifecycle event should teach or orient without rejecting | catalog block in the appropriate prompt surface |
| Completion banner | asynchronous host work changed state | typed completion item and optional next-turn signal |

The channel follows the event lifecycle. A tool argument error is immediate; a closeout-grounding failure discovered after the model stops is a next-turn nudge; a background scan completion is a completion item.

## Compact reject block (spec posture)

The spec-posture template ([`reject/spec-posture-block.md`](../lycaon/config/packs/painted-wolf/platform/guidance/reject/spec-posture-block.md)) renders:

```text
>>> Spec posture blocked
Tool: task
Blocked at: Phase 6 — Plan approval
Cause: The current plan is awaiting approval.
Fix: Wait for the user to resolve the active plan approval card
Required action: obtain plan approval before dispatching workers
Progress: 3/7 tasks complete
Code: SPEC_POSTURE_NOT_APPROVED
```

`What:`, `Cause:`, `Why:`, `Fix:`, and `Instead:` are the matched `policy/<CODE>.yaml` copy fields and render only when authored. The phase line comes from the posture rule's `phase_required` / `phase_required_name`, and `Required action:` from its `min_required` (plus `max_playbook`), all from the matched unit in [`posture-rules/`](../lycaon/config/packs/painted-wolf/platform/host/posture-rules) ([`spec_posture_reject.go`](../lycaon/internal/guidance/spec_posture_reject.go)). A `Details:` appendix carries the progress checklist once per session per checklist hash.

The `Code:` line is the branching contract; labels and prose are presentation. The model also receives bounded `tool_feedback` JSON beside the display copy ([`tool_feedback_projection.go`](../lycaon/internal/llm/transcript/tool_feedback_projection.go)): structured reasons, subjects, and exact native replacement calls as host-produced data, without granting authority. Oversized details are marked `details_omitted`. The ordinary outbound secret screen covers these fields.

A block answers four questions without narrating implementation: which transition failed, which observed fact caused it, which invariant refused it, and what bounded action resolves it.

### Typed detail conventions

- Browser action failures retain the original zero-based `index` and a bounded `completed_actions` prefix (index, type, locators per completed step). Those effects remain applied; the failing step may also have taken effect before settling failed. `interactive_controls` retains observed control names, text, and disabled state; `interactive_truncated` says the inventory is partial. An absent disabled value is unknown, not enabled. Successful idle waits return the settled control state.
- Owner failures caused by a subprocess deadline carry `timeout` details: measured `elapsed_ms`, the effective `deadline`, and `source` (`command` or `caller`) ([`owner_failure.go`](../lycaon/internal/tools/owner_failure.go)). They describe the underlying execution, which can predate a tool that joined shared work, and never establish a root cause or permission to repeat effects.

## Reachable-remedy rule

Feedback must not tell an agent to call a tool it does not have, approve its own request, write outside its scope, or bypass a workflow gate. Remedy rendering receives the current tool/worker surface and selects only reachable paths. If no direct remedy exists, the feedback names which party must act (coordinator, user, or a device setting) and the agent returns that dependency rather than looping.

A named **field** is reachable on the same terms as a named tool. In card copy, an unqualified backticked field name refers to an argument accepted by the receiving tool; name the owning tool when referring to another tool's field. Three things hold this together:

- The reject formatter refuses to render a card for a tool its `selector.tool` excludes, so a remedy authored against one tool's fields cannot reach another.
- Reject producers pass the receiving tool's accepted argument names, and each fix branch is conjoined with the field it would send the agent to. A pty has no `pipeline`, so `COMMAND_NOT_ARGV` points it at `command` instead.
- [`reject_copy_reachable_fields_contract_test.go`](../lycaon/test/contract/agentcontext/reject_copy_reachable_fields_contract_test.go) checks both directions against the schemas and the live producers.

An unreachable remedy is worse than none: the agent retries it, the schema refuses again, and the pair loops until the turn dies.

When a call names a tool whose schema is absent, the compiled tool plan determines
recovery. A deferred tool with `request_tools` offered returns `TOOL_NOT_OFFERED`
with `tool_loadable: true` and an exact `replacement_calls` entry that loads its
schema. The model retries on the next call, after loading; the rejected action is
never executed automatically. A name without that loading path receives an
unavailable-capability explanation. Loading changes schema visibility, not access
or approval. This guidance appears only on rejection, without expanding standing
prompts.


## Envelope reject codes

Native Go and confined MCP tools expose structured rejection codes through the shared envelope. MCP codes are read from registered typed error fields, never scraped from error prose. The same observation populates OAR facts (tool identity, call success, structured error code) so policy composes facts without a provider-specific string matcher. Unregistered external tools remain opaque results; the host does not invent a code by classifying their text.

An already rejected operation pins its original typed code in the policy context. OAR evaluates all selected rules using the declared anchor, selector, and condition; the renderer retains the original code and details when a different policy decision fires or its presentation is unavailable. Declared external MCP errors map to `MCP_CALL_FAILED` with the external code preserved as a detail and typed cause.

## Coordinator nudges

Post-turn nudges carry OAR or grounding decisions that could not reject an already-finished generation. All matched advisories are retained in evaluation order; the envelope identifies the primary code.

Delivery routes each resolved advisory once ([`advisory_delivery.go`](../lycaon/internal/oar/advisory_delivery.go), [`policy_advisories.go`](../lycaon/internal/session/policy_advisories.go)). Warnings at `tool.post_invoke` and `credential.assignment` stay attached to the tool result; `worker.finalize` advisories travel in the parent envelope. Everything else enters an acknowledged policy queue, separate from lifecycle kicks, that the model loop drains on every iteration and after model-input evaluation, so feedback raised during a tool call reaches the next model request without waiting for a user turn.

Queued entries retain anchor, qualified rule, effect, code, details, subject, and frozen display copy. Every occurrence is kept (OAR-EVAL-20); repetition policy belongs in OAR conditions and counters, not the queue. Persistence precedes acknowledgement, retries reuse the message ID, and model history receives the stored, screened message. Clearing a stopped or rewound task clears its pending feedback. Lifecycle-kick capacity eviction never discards queued policy feedback.

The default subject for a coordinator-only advisory is its session. A rule that observes a task, phase, worker, or resource supplies that target instead, so a changed subject is not suppressed as a duplicate.

Host lifecycle teaching (phase state, progress, roster, closeout) is not a nudge. It belongs to the corresponding informational anchor and current turn frame.

## Completion banners

Asynchronous work emits typed completion facts: operation identity, terminal state, affected resource, and any result handle. Den projects those facts as an activity or transcript item. The next model turn receives a compact guidance block only when the completion changes what it can or must do.

Success is never inferred from a friendly message. The producer states success or failure in the result envelope, and the host persists that state before rendering copy.

## Tool-output steering

Some successful tool results include structured state updates such as queue position, active task, new revision, or no-op reason. Prompt steering consumes those fields directly. A no-op remains a result; if the agent must branch, the result needs a structured discriminator, not a parse of “nothing changed.”

### Review verdict rejections

`submit_verdict` rejects with a `SUBMIT_VERDICT_*` code and typed details. Its arguments have three top-level channels: `verdict`, `cited_evidence`, and `cited_urls` ([`submit_verdict_tool.go`](../lycaon/internal/workflow/submit_verdict_tool.go)). The active phase schema exclusively defines the keys inside `verdict`, so misplaced citation channels and stale phase fields reject instead of being silently ignored. The two citation channels anchor the evidence a verdict rests on and are never projected as members of it.

A `claims`-typed member is a JSON array of `{id, title, statement, status, answers, cited_evidence}` ([`review_loop_verdict.go`](../lycaon/internal/workflow/review_loop_verdict.go)): `id` and `statement` are required; the phase that introduces a claim sets its one-line `title`; each `cited_evidence` entry names exactly one of `handle` or `path`. A later phase adjudicates an earlier claim by restating its id with the status word that phase's guidance teaches; the host stores the word and never interprets it.

The rejection details include the active phase, exact expected call shape, and bounded offender or reviewer facts. Correct the named condition and resubmit. A verdict rejection is not a completed review round and does not authorize abandoning the phase exit.

## Spawn and history hygiene

Worker creation and history reads produce their own structured outcomes: invalid or unavailable worker type, concurrency or wave limit, incomplete task charter, forbidden scope or resource, stale batch state, missing result or evidence handle, and history trimmed but still addressable. The remedy points to the batch or task assignee and never suggests bypassing the ledger with an untracked parallel action.

## Deduplication

Immediate rejections are returned for every failed invocation. Lifecycle guidance and banners may deduplicate identical rule/subject events within their declared lifecycle window. OAR advisory delivery preserves every resolved occurrence; its rules own repetition through conditions and counters.

Dedup keys use codes and typed identities, never rendered prose. A changed target, rule, task, phase, or remedy surface is a distinct event.

### Repetition and response boundaries

Repetition guards count model responses, identified by the assistant message ID.
Calls submitted together cannot react to one another's feedback. An identical
invocation contributes once per response, and its checks use the counts from
before that response. Cross-argument rejection tracking likewise counts each
response once per tool and code (`paintedwolf.code_reject_responses`). Every
rejected call still retains its original diagnostic and structured feedback.
Retries in later responses remain subject to the normal repetition thresholds.
Cross-argument counts saturate at the escalation threshold, bounding retained
response identities; the warning reports that lower bound as “at least.”

When the current request offers a previously unloaded tool, the host clears that
invocation's `TOOL_NOT_OFFERED` retry history. Other rejection histories remain
intact; schema loading does not resolve an access, argument, or workflow refusal.

## Authoring a new feedback path

1. Identify the transition operation and lifecycle anchor.
2. Define or reuse a stable structured code and detail schema.
3. Register the observable fact for OAR only if policy needs it.
4. Add the catalog binding and template.
5. Render remedies from the actual agent surface.
6. Verify immediate and queued delivery, deduplication, and persistence.
7. Regenerate the agent-public registry: `./task codegen:guidance-registry` re-derives [`schemas/guidance_registry.json`](../schemas/guidance_registry.json) from the stock packs through the production load path (`oar.SyncRegistryFromStock`). Undeclared facts, unsupported copy constructs, and invalid rule dependencies fail before output is written. The registry is codegen output; the pack `policy/` unit is what you author.

Do not create a second copy registry in tests or documentation. Contract validation derives coverage from the production registry and rendered templates ([`guidance_emission_contract_test.go`](../lycaon/test/contract/architecture/guidance_emission_contract_test.go)).

## Invariants

- Agents branch on codes and typed facts, not prose.
- Catalog templates provide explanatory sentences; anchors determine timing.
- Remedies are reachable from the recipient's current surface.
- Tool success and no-op state are producer facts.
- Deduplication uses structured identity.
- Feedback never teaches a bypass of the enforcing boundary.
