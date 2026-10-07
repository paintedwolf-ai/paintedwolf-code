# Agent prompt templates

Agent prompts are composed from versioned catalog templates over typed host facts. Templates teach behavior and present current state; they do not create tools, permission, workflow state, or enforcement.

**See also:** [Prompt assembly](prompt-assembly.md) · [Open Agent Rules](open-agent-rules.md) · [Agent tool feedback](agent-tool-feedback.md) · [Coordination](coordination.md) · [Workflows](workflows.md)

**Machine truth:** `packs/painted-wolf/<pack>/{agents,guidance,shared,host/bindings}/` units, repeated per pack (for example `implement/agents/`, `plan/guidance/`; there is no single shared catalog root) · [anchor catalog](../lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml) · [`schemas/anchors.schema.json`](../schemas/anchors.schema.json) · [`schemas/anchor-binding.schema.json`](../schemas/anchor-binding.schema.json) · [`prompt-budgets.yaml`](../lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml) · [`_persona-contract.yaml`](../lycaon/config/packs/painted-wolf/platform/agents/prompts/_persona-contract.yaml) · renderer in `lycaon/internal/prompts/`

## Why prompts are composed

One monolithic system prompt would mix stable role identity with workflow-specific instructions, current host facts, and temporary feedback. Composition keeps each concern isolated and replaceable:

```text
role contract + playbooks + workflow surface + current host facts + timely guidance
```

The resolved prompt source captures every template byte: catalog units plus the admitted site/project override snapshot. A turn frame supplies facts. Rendering joins the two at a known revision; that revision participates in prompt-cache identity and is reported by the authoring tools.

## Anchor catalog (lifecycle SSOT)

Anchors name lifecycle moments: `board.changed`, `tool.pre_invoke` / `tool.post_invoke`, `phase.exit_required`, `turn.closeout`, `worker.finalize`, and the `inject.*` prompt surfaces, all declared in the [anchor catalog](../lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml).

The catalog defines timing. Host code emits an anchor and its typed envelope; bindings decide which guidance template applies. A rule guard and an informational prompt therefore share one observation without duplicating when it occurs.

**Emit-time fields** describe the actual event (tool identity, rejection code, phase, receipt, task, result status) and are frozen when the anchor fires. Bindings require their declared selector facts; a missing fact is not a match. **Render-time fields** are presentation context such as current limits or display labels; they may be loaded when queued guidance renders but cannot change the event's meaning. Event identity never comes from template text: if a template needs a fact, the envelope carries it.

## Three-layer worker model

| Layer | Responsibility | Why separate |
|---|---|---|
| Archetype | stable role, method, tool discipline, output posture | shared across many workers |
| Playbook | reusable domain procedure | composes expertise without copying the role |
| Agent leg | concrete task lane, profile, selected skills, small delta | binds the role to one dispatchable worker |

The agent row selects its archetype, playbooks, tool profile, skills, and template, and contains only the delta that makes the role distinct. Archetype and agent ids are catalog vocabulary. Agent rows live with the pack that contributes the agent, so installation and removal keep identity and prompt bytes together.

## Coordinator model

The coordinator uses a separate prompt standard because it orchestrates rather than performing a bounded task. Its stable contract covers preserving user intent and authority, choosing direct versus delegated execution, assigning complete worker charters, tracking the batch and evidence ledger, asking the user only through the host decision channel, synthesizing worker findings, and closing against workflow gates.

Current workflow, roster, limits, resources, and workspace roots come from one immutable turn frame. The coordinator template and its injects do not rediscover them.

## Assembly order

Later layers may narrow or contextualize earlier ones but never silently replace their authority:

1. content-authority boundary;
2. role/archetype contract;
3. selected playbooks and agent delta;
4. active workflow and phase surface;
5. tool, agent, and host-resource rosters;
6. current obligations, decisions, and grounding state;
7. timely anchor guidance;
8. canonical conversation projection.

Project prompt overrides replace named template references under the trust rules. They do not insert an extra authority layer ahead of the system boundary.

## Template surfaces

| Surface | Examples |
|---|---|
| Identity | archetypes, agent prompts, coordinator contract |
| Procedure | playbooks, skills, workflow phase instructions |
| State explanation | active workflow, board, roster, gates, receipts |
| Feedback | rejection blocks, nudges, completion banners, notices |
| Shared language | partials for evidence, safety, checkpoints, and reporting |

Shared partials are for genuine language contracts used in more than one rendered surface, not a place to hide unrelated prose or bypass the declared template source.

## Template vs host boundary

Templates may explain typed state, teach a sequence or decision rule, name available structured tools and remedies, format bounded collections, and point to evidence and workflow obligations.

Templates may not add a tool or worker to the effective roster, claim a gate passed, grant filesystem or network authority, decide a host transition, infer state from transcript text, or weaken a structured rejection.

Host enforcement proceeds even if a template is absent or poorly worded. Template validation makes poor wording visible; it is not the security boundary.

## Required headings

Rendered agent prompts use a small required heading contract so roles remain inspectable through composition. The heading set and required partials live in [`_persona-contract.yaml`](../lycaon/config/packs/painted-wolf/platform/agents/prompts/_persona-contract.yaml); byte budgets live in [`prompt-budgets.yaml`](../lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml) and [`render-budgets.yaml`](../lycaon/config/packs/painted-wolf/platform/host/render-budgets.yaml).

Required headings describe semantic responsibilities (role, task method, tools/limits, evidence, result), not a fixed paragraph layout. Runtime persona rendering validates the final composed output, because validating source fragments cannot prove the rendered prompt is coherent. Failures name the artifact and violated contract.

## Prompt signal deduplication

One fact has one primary explanation per turn. Stable role guidance belongs in the role template; current phase state in the workflow block; event-specific correction in anchor guidance. Deduplication uses structured anchor/rule identity and turn state, never prose comparison. If two templates repeat a rule, move it to its authoritative template.

For the coordinator, the turn recipe owns action order, the visual-evidence section owns capture selection and procedure, the execution-mode shell owns delegation and progress, the evidence block owns claim requirements, and the final-report partial owns presentation fields and closeout review. Short cross-references connect these sections; they do not repeat the procedure.

### Reachability and progressive disclosure

The loaded tool schema and the reachable capability set are different facts. Schemas not on the call list under "Loadable tools" by name with a bounded description, and `request_tools` loads them; requesting a schema does not grant permission. Initial capability guidance covers loaded and requestable tools so the agent can select an action and understand sandbox boundaries. Detailed operating procedures use the exact tools offered in the provider request, after activation, resource projection, and tool-set narrowing. Execution tools receive runner recovery instructions; `http_request` receives request, cookie, redirect, and response-handling instructions. Secret-reference handling stays in the initial prompt because it also governs other consumers.

`inject.tool_procedures` renders that operating block through the anchor binding catalog. Request assembly inserts it after the leading system blocks and before conversation history, pins it through deterministic context fitting, and never stores it in the transcript. It is rebuilt per call, so activated tools receive their procedure before use, narrowed tools lose it, and compaction cannot remove it. Render failure stops the request rather than offering tools without their procedure.

Runner recovery rows name only offered tools whose generated tool contract declares the capability; prompt conditions explain those facts without granting authority.

Recall validation must cross an actual compaction boundary. The durable view covers an ordinal prefix: updates to newer messages do not invalidate it, while mutations inside the prefix do. Recall loads preserved evidence bodies from their project content blobs and reports storage failures as degraded results, and its bounded response is not immediately re-compacted. Use the stateful evaluation corpus described in [dev tasks](dev-tasks.md#tool-use-evaluation-evaltool-usage); a correct final answer is not sufficient if canonical history reappeared.

Tool schemas own argument definitions and limits. Prompt inventories show required signatures, not a second optional-argument catalog. Procedural teaching stays explicit where schemas are insufficient: the survey ladder, when to use `summarize`, how to interpret anchors and digests, follow-up/stop rules, and sandbox capability declaration and same-operation recovery. For explanation, `summarize` is the starting read of a named file, package, or directory; exact facts, quotes, and edits still use bounded reads, and its implementation windows are source evidence. Repository mapping is exposed through `list_dir({"path":"."})`; depth or pagination arguments select a literal listing instead.

Skills are selected task procedures under host policy and user scope, never extra authority. The stable prompt teaches the `skills_read` procedure without listing skills. The tool resolves a plain-language need to one skill in the effective catalog and opens its body immediately. A subsequent call with the resolved skill identifier and a relative resource path opens a referenced file. Resource availability and policy filtering still apply. Pack authors may opt reference files into policy-template rendering with the pipe-separated `metadata.paintedwolf.template_resources` field; unmarked references and all project resources remain literal.

## Variables and bounds

Callers assemble typed render variables. Templates read what they need; the renderer does not maintain a secret allowlist that can silently omit new facts.

All collections are bounded before rendering. When rows are omitted, the data includes a count and the template states that the view is partial. Model-authored or repository-sized collections never rely on template slicing to appear complete.

Prompt budgets are an authoring and release guard measured against the final render in UTF-8 bytes, not a runtime gate: render variables grow legitimately with the device and project. A failing cap names the artifact so authors can remove repetition at the correct layer. Bulk cap refreshes check the complete static stack before writing, preserving the configured model window and reserved session context.

## Overrides

Site and trusted-project `prompt_files` may override declared template references. Resolution is explicit by ref and follows project trust policy. Includes remain inside the admitted template environment and cannot escape to arbitrary filesystem paths.

Overrides teach the model under the same host boundary. They cannot override Go-defined facts, OpenAPI, tool schemas, workflow machine state, or authorization. An override that fails to parse or render is rejected with attribution; the host never falls through to a partially mixed prompt.

## Authoring loop

1. Identify the authoritative template or renderer for the sentence.
2. Reuse an existing anchor or template reference when the lifecycle moment already exists.
3. Add typed facts to the emitting subsystem when needed.
4. Keep host decisions out of template conditionals.
5. Bound collections in the caller and state omissions.
6. Render the full persona or coordinator prompt.
7. Check required headings, budget, duplicated signals, and available-tool honesty.

Render tests exercise capability presence/absence, deferred-versus-loaded behavior, roster membership, and final composition. Mock/fixture tests establish routing and prompt contracts; they do not establish that a model reliably selects the right skill or tool.

### Accuracy and invariant checks

Separate behavioral coaching from host guarantees. A prompt may ask for stronger validation or narrower execution than the host admits; describe that as an expectation, not a hard gate, an unavailable next action, or a guarantee about side effects.

Project changing contracts from their owners. Review-loop instructions carry the complete manifest verdict schema through `ProjectPhaseExit`, `InjectView`, and the active-workflow DTO ([`phase_exit.go`](../lycaon/internal/workflow/phase_exit.go)); the current schema is included on every model request. Active workflow instructions, worker instructions, and the spawn roster are request-local context, so they remain present across tool iterations and history compaction. Workspace-change guidance resolves each session's active root through the same resolver tools use.

Use system-derived tests rather than prompt snapshots or required sentences:

- [`verdict_projection_contract_test.go`](../lycaon/test/contract/agentcontext/verdict_projection_contract_test.go) walks the workflow catalog, generates admitted submissions (including empty claim arrays), adds fields, and checks required-field rejection and cache invalidation.
- [`pongo_loop_binding_contract_test.go`](../lycaon/test/contract/agentcontext/pongo_loop_binding_contract_test.go) checks catalog loop selectors against the pinned Pongo implementation. Pongo field names are case-sensitive; unknown fields otherwise render silently empty.

These tests protect structured contracts, not the truth of arbitrary prose. Do not add heavyweight service fixtures merely to lock down a shell recipe, or tests that assert a preferred sentence. If a procedure needs a machine-enforced guarantee, first model that guarantee as a typed contract.

### Previewing a render

`pw prompts render <template-ref> [--agent ID] [--project DIR] [--var k=v]… [--check] [--json]` ([`cmd/lycaon/prompts.go`](../lycaon/cmd/lycaon/prompts.go)) uses the runtime resolver and template engine, so the preview is the same composition path a session uses, and reports the resolved prompt revision. `--check` validates persona contracts and needs `--agent`. `--var key=value` parses a valid JSON value as its type; other text stays a string (`--var profile_has_command=false`, `--var 'offered_tools=["verify"]'`, `--var 'label="false"'`). When reviewing unbuilt bundled edits, set `LYCAON_CONFIG_ROOT` to the Go module directory containing `config/`; otherwise the binary's bundled configuration is authoritative. Render both present and absent branches.

`./task logs:dump -- --fixture implement_investigate_idle` shows the ordered coordinator layers with stock capability and workspace facts. It is a synthetic trace, not a complete device/session prompt: skills, host context, and tool schemas are excluded. `./task logs:prompt -- --file <llm-requests.jsonl> <N> --tools` shows the captured request in model-visible order.

### Live evaluation corpora

With explicit live authorization, `./task eval:tool-usage` runs a corpus against an isolated captured sidecar (paths relative to `lycaon/`; `--corpus` and `--timeout` flags in [`cmd/lycaon-debug/eval.go`](../lycaon/cmd/lycaon-debug/eval.go)). Normal regression tests stay mocked. Inspect actual skill reads, tool arguments, results, and final claims; tool counts alone do not establish correctness.

- `test/fixtures/eval/prompt-guidance.yaml` with a disposable copy of the minimal project builds a terminal report without explicitly requesting a screenshot, then exercises cross-file investigation and tests-only verification without worker dispatch. Each task gets a new session; tasks in a run share the project. Check that the plan includes a Snapshot row, the final capture runs the product after the last edit, and the final report presents the producer artifact. The profile's visual metrics count capture attempts, including sealed `command` captures and the default screen observation from `terminal_open`/`terminal_send`; they do not establish success or freshness.
- `test/fixtures/eval/exploration-guidance.yaml` against a disposable root containing copies of `internal/prompts`, `internal/summarize`, and `internal/repomap` as sibling directories separates unfamiliar-repository mapping, named-file/package explanations, an exact-symbol lookup, and an explicit `summarize` smoke test. Check whether mapping uses the bare-root view, explanations start with a briefing, exact lookups avoid unnecessary surveys, and follow-up reads close source gaps rather than duplicate returned windows. A successful explicit request proves the tool works, not that models choose it unaided. For language comparisons, freeze the source tree, model configuration, and corpus in both arms and change only the named prompt/schema descriptions.
- `test/fixtures/eval/secret-guidance.yaml` against a disposable copy of `test/fixtures/eval/secret-consumer` checks secret-use teaching in the initial coordinator prompt while lifecycle tools are deferred: the consumer must accept the correct reference, reject a distinct incorrect one, and refuse a revoked reference. Inspect canonical arguments for references rather than values. The corpus uses newly generated test credentials, never existing ones. Allow a realistic per-task `--timeout` (for example `20m`) for multi-call trials and approval latency, and approve only the exact test-consumer action; never grant task-wide or project-wide access to make a trial finish.

The profile labels the first surfaced coordinator model/provider pair, falling back to the first coordinator call. Token totals include auxiliary calls and workers; a preflight model is not the agent being evaluated.

## Prompt context diet

Prompt content is proportional to the role and current stakes: stable principles once, current facts rather than history, exact structured remedies rather than exhortation, optional procedures read only when needed, bounded tool rosters, and evidence requirements only when the active phase declares them. Leaf work does not need a repository-survey sermon; high-risk build or repair work does need explicit evidence and verification obligations. The workflow and host facts decide which surface is active; templates explain it.

## Invariants

- Catalog templates supply instruction text; host code supplies typed facts.
- One turn frame feeds every current-state render.
- Anchors schedule both guards and guidance.
- Each composition layer has a distinct responsibility.
- Overrides change instruction content but never host authority.
- Rendered collections are bounded, inspectable, and honest about omissions.
