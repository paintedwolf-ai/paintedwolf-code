# Agent contract

Branch on the structured reject `Code:`, never on free-form prose.

**See also:** [Agent tool feedback](agent-tool-feedback.md) · [Host behavior](dispatch-hints.md) · [Prompt attachments](prompt-attachments.md) · [Docs map](README.md)

**Machine truth:** [`schemas/guidance_registry.json`](../schemas/guidance_registry.json) · stock pack `policy/` dirs under [`lycaon/config/packs/painted-wolf/`](../lycaon/config/packs/painted-wolf) · [`posture-rules/`](../lycaon/config/packs/painted-wolf/platform/host/posture-rules) · [`ApiErrorCode.yaml`](openapi/vocab/ApiErrorCode.yaml)

---

## Core rule: branch on codes, never interpret

When a tool response or envelope carries structured guidance:

1. Extract the code from the structured surface that carried it:

   | Surface | Where the code is |
   |---------|-------------------|
   | Reject block in a tool-role message | the `Code:` line ([`hostmarker.CodeLine`](../lycaon/internal/hostmarker/hostmarker.go)) |
   | Tool result on the wire | `feedback[].code` — ordered `ToolFeedback{code, details, subject}`; `codes[]` is its compact ordered projection for card selection, never the only rejection data ([`tool-results.yaml`](openapi/components/schemas/session/tool-results.yaml)) |
   | Per-tool JSON envelope | the envelope's own `hint_code` field (`list_scans`, `list_dir` map diagnostics, worker results), rendered into the agent-facing line by [`guidance.EnvelopeHintMessage`](../lycaon/internal/guidance/envelope.go) |

2. Look up the code in the plane that publishes it:

   | Plane | Lookup |
   |-------|--------|
   | Guidance / OAR codes | [`schemas/guidance_registry.json`](../schemas/guidance_registry.json), the generated union of every stock pack's `policy/<CODE>.yaml` |
   | Posture denials | [`posture-rules/*.yaml`](../lycaon/config/packs/painted-wolf/platform/host/posture-rules) — see [Posture codes](#posture-codes) |
   | Attachment and prompt-intake codes | `platform/host/user-notices/<CODE>.yaml` plus the closed `ApiErrorCode` vocabulary (`docs/openapi/vocab/ApiErrorCode.yaml` → generated [`api_error_code_ids.generated.go`](../lycaon/pkg/api/api_error_code_ids.generated.go)). Not in the guidance registry; [`prompt-attachments.md`](prompt-attachments.md) is their contract page |

3. Follow **`branch_instruction`** from the registry (authored as `instead` in the pack YAML). Do not infer meaning from error text.

## Reject block format

Two shapes appear in tool-role messages: the spec-posture compact block (`ToolRejectFormatter`) and the non-spec unified block (`StaticRejectFormatter`). Branch on `Code:` in either case; nothing else in the block is a contract. Both layouts and the session-deduped `Details:` appendix are specified in [`agent-tool-feedback.md` § Compact reject block](agent-tool-feedback.md#compact-reject-block-spec-posture).

## Doom-loop codes

| Code | When | Action |
|------|------|--------|
| `DOOM_LOOP_REPEAT_WARN` | 3rd through 8th identical completed tool+args | Change approach now |
| `DOOM_LOOP_REPEAT` | 9th identical tool+args (`DoomLoopMaxAttempts + 1`) — blocked | Stop; ask for clarification |
| `DOOM_LOOP_REPEAT` with `data.code` | 3rd identical attempt after two consecutive rejects carrying the **same** `Code:` — blocked before the count rule | Fix what that code named; a verbatim replay cannot succeed |
| `DOOM_LOOP_CODE_REPEAT` | 3rd rejection of one tool under one `Code:` this session, counted across **different** arguments | Take the action the reject states (it carries the repeated code's own `instead`) or drop the tool arc |
| `DOOM_LOOP_FRUITLESS_SEARCH` | 3rd consecutive search for the same question returning nothing | Ask a different question or read the file directly |

Thresholds live in [`loopguard/doom_loop.go`](../lycaon/internal/session/loopguard/doom_loop.go): `DoomLoopMaxAttempts = 8`, `DoomLoopMaxSameCodeRejects = 2`, `DoomLoopMaxCodeRepeats = 3`, `DoomLoopWarnAfterCompletions = 3`. The warn threshold has no non-test reader in Go: its policy unit's `when` fires the warn, and [`doom_loop_thresholds_contract_test.go`](../lycaon/test/contract/host/doom_loop_thresholds_contract_test.go) pins the constants to the `security/policy/DOOM_LOOP_*.yaml` units. `DoomLoopMaxCodeRepeats` is also enforced in Go (`internal/session/coordinator_wire.go`).

An identical call counts as a repeat only while it stays consecutive among **effectful** calls: those whose contract lifecycle is a DB transaction, journaled mutation, durable job, or effect attempt. A successful effectful call by another tool resets the count, so rebuilding after an edit is not a repeat. Read-only surveys and ephemeral controls do not reset it. Held-page tools compare by the target behind the `id`, so reopening a page does not begin a new count.

The two same-`Code:` rules count different things. `DOOM_LOOP_REPEAT` with `data.code` needs consecutive rejects of a byte-identical call; `DOOM_LOOP_CODE_REPEAT` accumulates across every argument shape and survives a successful call in between, so rewording a rejected call does not reset it.

## Workflow start codes

| Code | Meaning |
|------|---------|
| `WORKFLOW_START_REQUIRES_HUMAN_APPROVAL` | Coordinator `state_start` recorded a proposal; wait for a slash, drawer, API, or proposal-card Start action ([`session.md`](session.md)) |

Human paths (slash commands, `POST …/workflow-runs`) do not pass through this guard.

## Remote-package execution codes

Downloaded package code runs under a non-widenable reduced boundary. Branch on the code instead of treating a blocked package destination as an ordinary network approval:

| Code | Action |
|------|--------|
| `REMOTE_PACKAGE_EXECUTION_REQUIRES_FRESH_BOUNDARY` | Run the package action with `command`, not inside an already-running terminal |
| `REMOTE_PACKAGE_EXECUTION_CAPABILITY_DENIED` | Remove the extra capability; separate package acquisition from the later local-tool action |
| `REMOTE_PACKAGE_EXECUTION_DESTINATION_DENIED` | If the denied connection was required, install or add the package first, then invoke the installed local executable in a later command; keep an otherwise successful result when the attempt was optional |

Every affected process result carries `remote_package_execution` with the applied environment, credential, sensitive-read, network-scope, and allowed-host facts ([`confine/report.go`](../lycaon/internal/confine/report.go)). Approval reuse never removes that report or widens the boundary.

## Posture codes

`RuleEngine` denies at invoke from the declarative rules under [`posture-rules/`](../lycaon/config/packs/painted-wolf/platform/host/posture-rules): one file per session posture, plus `no_folder.yaml` for a project with no attached folder. The files are the whole published vocabulary and the enforcing source.

**Spec posture** ([`spec.yaml`](../lycaon/config/packs/painted-wolf/platform/host/posture-rules/spec.yaml)):

| Code | Meaning |
|------|---------|
| `SPEC_POSTURE_UNRESOLVED` | Establish spec posture (`/plan` or write a blueprint file) before high-risk tools |
| `SPEC_POSTURE_NOT_APPROVED` | Complete the plan approval gate before handing off to implement |
| `SPEC_POSTURE_STATE_FORBIDDEN` | State-tracking tools are build-only; use `write`/`edit` on the blueprint file |
| `SPEC_POSTURE_DELEGATION_FORBIDDEN` | Delegation tools blocked until the plan workflow completes |
| `SPEC_POSTURE_STUB_REQUIRED` | Write the phase 1 blueprint stub before `task` researchers |
| `SPEC_POSTURE_IMPLEMENT_FORBIDDEN` | Stay in spec-posture phases until the plan is approved and handed off |
| `SPEC_POSTURE_HANDOFF_FORBIDDEN` | Handoff tools blocked until the plan workflow milestones are met |
| `SPEC_POSTURE_SCOPE_REQUIRED` | Capture scope in phase 0 before spawning research or critic tasks |
| `SPEC_POSTURE_BREAKING_REQUIRED` | Capture breaking-change policy in phase 0 before those tasks |
| `SPEC_POSTURE_PHASE_SKIPPED` | This tier is skipped for the phase; do not spawn agents for skipped work |
| `SPEC_POSTURE_RESEARCH_REQUIRED` | Finish research and leave the research phase before critic tasks |
| `SPEC_POSTURE_REVIEW_DEPTH_REQUIRED` | Complete phase 4 review depth before critic tasks |
| `DISALLOWED_AGENT` | `agent_type` is not in the workflow's `allowed_agents` allowlist |

**Other postures:**

| Posture file | Code | Meaning |
|--------------|------|---------|
| `build.yaml` | `DISALLOWED_AGENT` | `agent_type` outside the workflow allowlist |
| `coordinator.yaml` | `WORKFLOW_ADVANCE_INACTIVE` | `workflow_advance` needs a running workflow run |
| | `WORKFLOW_TRANSITION_INACTIVE` | `workflow_transition` needs a running workflow run |
| | `WORKFLOW_DELEGATION_NOT_READY` | Delegation blocked until `implement_workflow_ready` |
| | `DISALLOWED_AGENT` | `agent_type` outside the workflow allowlist |
| `orchestrate.yaml` | `ORCHESTRATE_POSTURE_NO_PLAN_WRITER` | Orchestrate sessions use implementer / researcher agents, not `plan-writer` |
| | `WORKFLOW_DELEGATION_NOT_READY` | Complete topology stages before delegation tools |
| | `DISALLOWED_AGENT` | `agent_type` outside the workflow allowlist |
| `vet.yaml` | `VET_POSTURE_DELEGATION_FORBIDDEN` | Vet sessions do not dispatch implementers |
| `no_folder.yaml` | `PROJECT_HAS_NO_ROOTS` | File and shell tools need an attached folder; chat, web research, and plan-read tools stay available |

Rules deny; the guidance registry documents the agent-facing branch behavior for the same code. Two codes have no registry row: `ORCHESTRATE_POSTURE_NO_PLAN_WRITER` and `VET_POSTURE_DELEGATION_FORBIDDEN` are emitted with the rule's own `message`, so a registry lookup returns nothing. Branch on the code and read the posture file.

The vet posture's read-only boundary is not a rule. A vet turn resolves to an `observe_` coordinator surface, which carries no path-mutating or command-scoped tool to offer or defer, so a write is refused as `COORDINATOR_TOOL_DENIED` before any posture rule runs ([`coordinator-flow.yaml`](../lycaon/config/packs/painted-wolf/platform/host/coordinator-flow.yaml)).

## Completion tool banners

After completed host tool invocations, post-tool guidance may append `>>> Tool feedback` blocks to tool output. Codes include `SPEC_POSTURE_PROGRESS`, `SANDBOX_*` recovery (OAR `tool.post_invoke`), `VERIFY_UNVERIFIABLE`, `EDITORCONFIG_MISMATCH`, `DOOM_LOOP_REPEAT_WARN`, `BOARD_EMPTY_SKIP_TO_VERIFY`, and `WORKFLOW_GATE_BLOCKED`. The same branch-on-`Code:` rule applies. Catalog and dedup rules: [`agent-tool-feedback.md`](agent-tool-feedback.md).

Hints with `emit: banner` (for example `BANNER_TASK_QUEUED`, `BANNER_WORKER_INFLIGHT_ROSTER`) are informational and never block execution. Read them before proceeding.

## Context boundaries

Do not pass raw reject blocks between agent roles (coordinator ↔ worker ↔ scout). Sanitize context at role boundaries.

## Prompt tool surface

The callable set is compiled per turn: `ListForPrompt` plus activated open-world names ([`coordination.md`](coordination.md)). It gates invoke by surface (families expanded), resource-implied control tools, enabled MCP on open-world surfaces, and activated open-world names. Profile and posture gates still apply at invoke. A tool absent from the schema should not be called; a tool blocked at invoke returns a structured `Code:`.

## Human checkpoints

Unified bus: `GET/POST …/checkpoints` and SSE topic `checkpoint`. Branch on **`kind`** and resolve payloads, not free-form titles.

Tool permission is evaluated before this bus. A `tool_approval` checkpoint is human consent for an invocation that has already passed its profile and policy gates; its resolution does not add a tool capability. A path inside the app's own state tree never reaches this bus: the gate denies it with `Code: SANDBOX_CONTROL_PLANE_DENIED` from the same predicate the path resolver enforces. Tell the person the folder is not reachable by tools; do not retry it.

| Kind | Blocks | Resolve |
|------|--------|---------|
| `tool_approval` | At the boundary named by its immutable `plan.stage` | `approve` (+ required host-authored `option_id`) \| `reject` |
| `content_apply` | After the tool body is computed, before disk | `approve` \| `reject` \| `approve_partial` (+ host-authored `approved_hunks` ids) |

There is no checkpoint for writing into a file the person has open. That write lands in their editor document and saves, the same way their own save would; the receipt states where it landed ([tools.md § The open document is the file](tools.md#the-open-document-is-the-file)).

Every `tool_approval` carries one immutable `plan`: its exact subject, reviewed presentation, reasons, and affirmative options. An option may install several typed authority deltas atomically (for example socket plus direct-network access). Redaction is a host-authored option, not a client decision verb. Write-root and read-path asks come only from structured `capability_request.write_root` / `capability_request.read_path`, before spawn, as ordinary `tool_approval` plans with `stage: pre_spawn` and a `write_root_set` or `read_path_set` subject. Child stdout and stderr can never request or install authority. A resolved decision carries every reusable authority from the selected option in `grant_ids[]`; revoke treats the active ids as one selection.

Every `content_apply` carries immutable host-authored hunks. Partial approval selects their ids; the host validates the selection and composes the final bytes from the stored before-content before resolving the checkpoint. The client never submits alternate file content, and the native tool performs one final write.

Tool results when the user rejects:

| Kind | Tool result |
|------|-------------|
| `tool_approval` | Structured reject `Code: approval_denied` with copy from `approval-outcome-codes.yaml` ([`approvaloutcome`](../lycaon/internal/approvaloutcome/config.go)) |
| `content_apply` | Structured reject `Code: CONTENT_APPLY_REJECTED` (+ `data.path`), carrying the user's redirect guidance when they gave one |

A checkpoint that ends without a decision is not a refusal. `approval_expired` and `approval_canceled` name the non-decision outcomes of a `tool_approval`; for `content_apply`, `hitl.ErrContentApplyExpired` (review window elapsed) and `hitl.ErrContentApplyUnresolved` (canceled) are prose, not codes, and both say the edit was not applied. Expiry is retryable; a reject is not.

Treat a rejection as terminal for that tool call: adapt the plan or ask the human. Do not retry the same mutation without changed inputs.

## When-expression vocabulary

Conditional hints use the OAR condition language over typed facts. The shipped DSL is documented in [`guidance-conditions.md`](guidance-conditions.md), the engine contract in [`open-agent-rules.md`](open-agent-rules.md), and runtime evaluation lives in `lycaon/internal/oar/`. The rule document format is <https://openagentrules.org/spec/1.0/>.

## System prompt templates

Worker and coordinator personas embed this contract in the `agents/prompts/*.md` of the pack that ships the agent ([`agent-prompt-template.md`](agent-prompt-template.md)): branch on `Code:`; do not satisfy gates via chat prose.

## Related

- [`agent-tool-feedback.md`](agent-tool-feedback.md) — feedback channels, compact reject spec, completion banners
- [`agent-prompt-template.md`](agent-prompt-template.md) — prompt composition and required headings
- [`guidance-conditions.md`](guidance-conditions.md) — shipped condition vocabulary
- [`authorization.md`](authorization.md) — approvals and capability grants
- [`docs/schemas/events/envelope.json`](schemas/events/envelope.json) — SSE envelope shape
