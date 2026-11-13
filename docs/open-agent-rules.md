# Open Agent Rules

Open Agent Rules (OAR) is the typed observation-and-effect layer used for agent guidance and policy. Rules evaluate host facts at named lifecycle anchors and request a bounded host effect:

```text
typed host observation + declarative condition -> host effect channel
```

A rule does not execute code, parse transcript prose, grant capability, or mutate workflow state directly.

**See also:** [Agent tool feedback](agent-tool-feedback.md) · [Guidance conditions](guidance-conditions.md) · [Grounding](grounding.md) · [Agent contract](agent-contract.md) · [Authorization](authorization.md) · [Extend](extend.md)

**Machine truth:** [`schemas/oar/`](../schemas/oar) (vendored rule, selector, and anchor profile map schemas, the vocabulary, and the conformance corpus) · `lycaon/internal/oar/`, `oarcore/`, `oarcopy/` · pack `policy/` units under [`lycaon/config/packs/painted-wolf/`](../lycaon/config/packs/painted-wolf) · [engine declaration](../lycaon/config/packs/painted-wolf/platform/host/anchors/oar-profile.yaml) · [anchor catalog](../lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml) and `host/bindings/` · spec: <https://openagentrules.org/spec/1.0/>

Painted Wolf Code implements Open Agent Rules 1.0 in full: document loading,
the closed typed fact environment, evaluation, copy rendering, hints, and the
conformance corpus. Every copy member is optional. The host may present a
code-only outcome; it must not turn omitted copy into an invented remedy.
Host-specific knowledge facts distinguish unreported observations from measured
zero without changing the standard fact defaults.

## Conformance claim

This engine implements the core tier and both standard profiles,
`content-safety` and `mcp`, and passes every fixture in the vendored corpus.
`internal/oarcore` is the standard's engine: closed document model, condition
checker and evaluator, copy grammar, selector matching, and the fact
environment. `internal/oar` binds it to the host: fact providers, anchors,
counters, detectors, transforms, and delivery.

The [engine declaration](../lycaon/config/packs/painted-wolf/platform/host/anchors/oar-profile.yaml)
carries the published anchor profile map (`profile_map`, validated against
`oar-profile.schema.json`) together with everything the engine declares before a
rule compiles: the profiles it claims, its host anchors, every `paintedwolf.*`
host fact with its type, the detectors it resolves, and its limits.

## Why rules are separate from host code

Policies such as when to nudge an agent to ground a claim, block an unsafe completion claim, or warn on a suspicious observation need to evolve as declarative content. Putting each case in control-flow code would mix facts, wording, timing, and enforcement. OAR separates them: host code observes and freezes facts; an anchor defines when evaluation is meaningful; a rule composes typed predicates; an effect maps to one host channel; a binding or template explains the result. The host stays authoritative because it supplies the facts and implements every effect.

## Runtime line

```mermaid
flowchart LR
    H[Host lifecycle anchor] --> F[Frozen fact snapshot]
    F --> R[Evaluate admitted rules]
    R -->|no match| N[Continue]
    R -->|allow| P[Explicit pass, no other effect]
    R -->|warn| W[Non-blocking host observation]
    R -->|nudge| U[Structured next-turn guidance]
    R -->|transform| T[Content redact / replace / annotate]
    R -->|block| B[Structured rejection]
```

Evaluation is deterministic over an admitted rule set and frozen fact snapshot. Selected rules request typed observations lazily; provider failures follow the rule's `on_error` policy. Model classification is not part of condition evaluation.

## Rule kinds

`kind` is one of `schema`, `policy`, `invariant`, `detector`. Per the schema, it "fixes evaluation order and nothing else": evaluation runs schema, then policy, then invariant, then detector. Kind says *when among its peers* a rule is checked, not why it exists or what it may do.

| Kind | Evaluates | Typical use |
|---|---|---|
| `schema` | first | shape and argument validation before policy reasons about input |
| `policy` | second | ordinary conditional rules — the bulk of pack content |
| `invariant` | third | host-level guarantees that must hold regardless of policy outcome |
| `detector` | last | project an observation (via a registered `detector://` reference) for later rules or provenance, never a verdict |

An engine must not derive a threshold, weight, or decision from `kind`; that belongs in `when` and `effect`. Kind and effect are independent axes.

## Rule document

A rule identifies itself, selects one or more anchors, declares a typed condition, and names an effect with structured parameters. Human title and rationale explain the decision; stable ids and codes carry identity.

Rule identity is the document's `namespace` and `id`, independent of filenames and package names. Duplicate identities across contributing packs are load errors, and configuration cannot disable or downgrade a rule whose `mandatory` is true ([OAR-OPS-5]). Structured rejection codes are agent-public: a retired meaning is never reused, and deprecation names its successor.

Unknown behavioral fields, facts, values, effects, or parameters fail validation. A rule names every non-core fact it reads, in `when`, copy, or a `ledger` selector clause, in `requires_facts`; a fact this host does not provide fails the load with its name.

Copy members (`what`, `cause`, `why`, `fix`, `instead`, `message`) are top-level fields that bind facts through the Appendix B grammar: `{{ fact }}` and `{% if [not] fact %}…{% else %}…{% endif %}`. `category`, `emit`, `scenarios`, `evidence`, and `context_schema` are informational and never affect a decision.

### Host extensions

Host behaviour the standard leaves to the implementation is carried in `x-paintedwolf-` fields, which other engines ignore:

| Field | Meaning |
|---|---|
| `x-paintedwolf-transform` | `action`, `target`, and `replacement` for a `transform` rule ([OAR-OPS-8]) |
| `x-paintedwolf-counter-scope` | A fact whose value keys an additional fire counter, read as `paintedwolf.scoped_fire_count` ([OAR-FIRE-4]) |
| `x-paintedwolf-audience` | Tool profiles that may receive the rule |
| `x-paintedwolf-status` / `x-paintedwolf-successor` | Deprecation and the code or boundary that replaces it |
| `x-paintedwolf-severity` | A presentation severity outside the standard's `block`, `warn`, `info` |
| `x-paintedwolf-category` | The category of a hint document, whose closed field set has none |

### Hints

A `kind: hint` document carries static copy for a code the host raises itself and is never selected or evaluated ([OAR-HINT-1]). A deprecated code whose rule no longer decides remains a hint so its meaning stays known.

## Anchors and bindings

An anchor names a host lifecycle observation: tool pre-invoke, tool post-invoke, coordinator turn closeout, phase exit, worker finalize, authorization decision. Anchors are the timing source for both guards and informational prompt injection: host code emits one anchor with typed facts, and bindings select templates for matched informational effects, so a policy check and its explanation cannot drift onto different moments.

The specification fixes eight core anchor ids; the anchor profile map binds each to a local catalog anchor ([`engine_declaration.go`](../lycaon/internal/oar/engine_declaration.go)). A rule's `on` names a core or host anchor. Rules authored against core anchors are the portable subset.

Selectors use the closed vocabulary of `selector.schema.json`. `tools` and `session_posture` read core facts; `profiles` reads the core `profile`; this host models `surface` (coordinator, worker, or phase), `surfaces`, `phase`, `workflow`, and `principal` from the session. A `ledger` clause matches declared typed host facts such as `paintedwolf.operation`.

| Catalog anchor | Runs | Notes |
|---|---|---|
| `tool.pre_invoke` | before a tool call is dispatched | core; scope with `selector.tools` |
| `session.pre_invoke` | before a prompt-loop tool call, with session posture, tool identity, and arguments observable | precedes coordinator-specific observations and the executor's core tool boundaries |
| `tool.handler` | after shared argument validation, before dispatch to the tool implementation | core |
| `tool.rejected` | when an intrinsic typed tool failure is raised, before its feedback is delivered | does not replay pre-invocation rules or undo effects of a completed call; publishes `paintedwolf.rejection_code` |
| `tool.post_invoke` | on the complete result, successful or failed, before logging and delivery | core; a block withholds the original result; a transform replaces it and clears parallel captures |
| `credential.assignment` | on one literal credential assignment observed on a completed tool call | measurements, harvest membership, device HMAC fingerprint |
| `coordinator.post_turn` | on the assembled final prose once, before turn completion | implements core `agent.post_turn` |
| `coordinator.mid_turn_note` | when a coordinator's text beside tool calls is withheld from the user | informs through the shared host feedback path; the tools in that response still run, and only an unresolved reference spends grounding friction |
| `coordinator.closeout_check` | on a provisional closeout claim | a block requests repair; provisional checks cannot multiply a portable rule's counters |
| `worker.finalize` | on the complete parent envelope before the worker card or digest is stored | implements core `agent.finalize`; a block delivers policy feedback with a partial status and withholds the report, proof, and grounding details |
| `worker.report_check` | on a provisional worker report | can request repair before final delivery; not the portable finalization boundary |
| `content.input` / `content.output` / `content.tool_result` | content-safety boundaries | core `model.input` / `model.output` / `model.tool_result` |

Model response prose is buffered until `content.output` resolves. The shared response classifier supplies non-executable text-action observations at this same boundary; `TOOL_ACTION_IN_TEXT` uses the existing bounded rejected-draft repair lifecycle for coordinators and workers. Live progress contains metadata; the final policy result controls transcript persistence, replay, and returned prose.

Non-blocking advisories queue the engine's frozen copy and structured identity; they do not require the rule to exist in the stock presentation registry.

Rules may subscribe only to anchors whose fact environment satisfies their condition. The validator catches impossible references before the pack becomes effective.

## Fact discipline

Facts describe observations, not conclusions the rule wishes were true. Good facts: the tool name and structured arguments the host admitted; whether the executor applied confinement; active workflow phase and open gate ids; evidence receipt counts and types; worker state from the coordination ledger; provider readiness from the model catalog; a structured MCP error code from an admitted binding. Bad facts: “the user probably wants deployment” inferred from chat; “the change is safe” because no keyword matched; “the agent researched enough” because it said so; “the destination was observed” when it was only declared.

Facts use neutral polarity. Prefer `verification_receipt_count = 0` over `agent_failed_to_verify = true`. The rule defines the policy interpretation and can combine the observation with phase, tool availability, and explicit exemptions, so a new policy does not need a new host boolean for every conclusion.

Intrinsic tool failures publish `paintedwolf.rejection_code` at `tool.rejected`; stock denial policies test that identity where a shared `policy_denied` flag would be ambiguous. Diagnostic `reason` and `field` values publish `paintedwolf.arg_validation_reason` and `paintedwolf.arg_validation_field` without replacing canonical tool or MCP identity. The core `profile` is the effective tool profile; `paintedwolf.session_role` says whether the occurrence belongs to a coordinator or a worker. `paintedwolf.principal` is the authenticated person, falling back to the stored session owner, and `paintedwolf.principal_roles` comes from the authenticated caller. Worker finalization resolves stored session facts only when a rule references them.

## Fact catalogue

The release gate checks fact signatures and retirement successors against the
published stable baseline. The structured retirement catalog is
[`agent-public-retirements.json`](../schemas/agent-public-retirements.json).

`paintedwolf.high_risk_tool` is deprecated and retired because its provider combined policy categories, contrary to OAR-FACT-7. Its named successor is the explicit rule expression `(paintedwolf.tool_is_state || paintedwolf.tool_is_delegation || paintedwolf.tool_is_handoff || paintedwolf.tool_is_task)`. The fact environment no longer declares the retired name; a rule naming it fails load with the undeclared identifier before evaluation. This is the published retirement rule for that agent-public fact; the name must never be reused. The independent host posture-condition vocabulary retains its existing authorization behavior.

`paintedwolf.relocated_field` and `paintedwolf.nested_under` are retired. Their successors are the explicit field list `paintedwolf.misplaced_fields` and the structural locations `paintedwolf.found_under` and `paintedwolf.belongs_under`. `paintedwolf.unparsed_json` is retired; use `paintedwolf.json_encoded` for a JSON string where a structured value belongs and `paintedwolf.json_malformed` for invalid JSON syntax. The retired identifiers are no longer advertised, fail rule loading, and must never be reused.


Internal occurrence facts are grouped into typed domains for invocation, session, progress, workflow, workers, execution, refusals, source, access, content, grounding, MCP, and counters. These Go domains are not standard profiles. The activation projection uses the declared flat fact names and types; lazy provider state and the pre-occurrence counter snapshot keep their own lifetimes ([OAR-FIRE-5]).

The fact environment is closed and declared before any rule compiles ([OAR-FACT-1]), in three tiers ([`catalogue.go`](../lycaon/internal/oar/catalogue.go)):

| Tier | Names | Provided |
|---|---|---|
| Core | `tool`, `tool_args`, `session_posture`, `profile`, `arg_validation_errors`, `tool_args_fingerprint`, `breaker_count`, `same_code_reject_run`, `is_directory`, `not_found`, `path_denied`, `policy_denied`; function `path_outside_scope` | at every anchor |
| Standard | `content-safety` (`prompt_injection_score`, `jailbreak_score`, `pii_entities`, `secret_matches`) and `mcp` (`mcp_server_*`, `mcp_tool_name`, `mcp_call_ok`, field accessors) | whole, as claimed in the engine declaration |
| Host | `paintedwolf.*` | as declared in the engine declaration |

Parameterized observations are typed functions over a declared argument, such as “receipt of this type exists”.

Parameterized observations are not arbitrary callbacks. Their names, argument types, cost, and result types are part of the closed environment. Evaluation cannot read the filesystem, network, database, or transcript beyond facts already captured for the anchor. Exact names and operators: [Guidance conditions](guidance-conditions.md).

## Conditions

Conditions compose typed facts with equality, membership, ordering where defined, boolean operators, the string builtins `starts_with`, `ends_with`, and `contains`, and bounded collection/observation functions. Absent observations take their declared typed zero. Unknown names and static type mismatches are load errors. Invalid runtime provider values, overflow, division by zero, and out-of-range indexing raise evaluation errors handled as the standard defines.

There are no regular-expression operators, and chat or transcript prose is not a fact. Text matching is allowed only on sanctioned event sources such as detection-pack command fields, outside this core fact floor.

## Effects and channels

`effect` is one of `block`, `warn`, `nudge`, `allow`, `transform`:

| Effect | Channel |
|---|---|
| Block | return a structured rejection from the transition being guarded |
| Warn | record and surface a non-blocking host observation |
| Nudge | queue structured user-role guidance for the next coordinator turn |
| Allow | record an explicit pass; contributes no decision and cannot override another rule's block, warn, or nudge |
| Transform | apply a declared content mutation (redact, replace, or annotate) rather than accept or refuse outright |

Across the shipped `painted-wolf` packs only `block`, `warn`, and `nudge` are in use; `allow` and `transform` are valid, schema-defined effects no shipped rule uses.

Evaluation runs in `kind` then `id` order. The first enforced rule firing with `block` or `transform` short-circuits the occurrence ([OAR-EVAL-3]); otherwise the decision is `block`, then `nudge`, then `warn`, then allow. Composition falls out of that order: a specific refusal sorted ahead of a general one wins without any rule naming another.

There is no `ask` effect. Nothing wires an OAR effect into the human-approval/checkpoint subsystem (`internal/approvals`, `internal/authzcontext`); a rule that wants a human decision expresses it as `block` with a remedy the human can act on, or `nudge`.

`inform` is a **binding**-side effect, not a rule one. The shared Anchor Binding grammar ([`anchor-binding.schema.json`](../schemas/anchor-binding.schema.json)) freezes `inform | block | transform` as the bus classes; `warn`, `nudge`, and `allow` are refinements of the block class, which is how `oar.schema.json` `$ref`s the grammar without forking it. The bundled inform bindings under `platform/host/bindings/` declare `effect: inform` with a `render:` template stem; that is the [anchors-and-bindings](#anchors-and-bindings) path for informational prompt injection. No rule document may say `inform`.

Rule kind does not restrict effect. A transform rule's `x-paintedwolf-transform` names the content it replaces and the replacement. The host delivers transforms only at `tool.post_invoke`, `content.output`, `content.tool_result`, and `credential.assignment`, and rejects them elsewhere ([`host.go`](../lycaon/internal/oar/host.go)): model input holds messages with separate roles and provenance, so a transformed concatenation would change their authority. Rule parameters can narrow copy and scope but cannot select arbitrary code or bypass the effect operation.

Multiple matching advisories are delivered in evaluation order. A rejection envelope carries one primary stable code while retaining the full matched decision set for diagnostics and audit.

## Stateful effects

Rules are pure. The engine or responsible subsystem stores counters, deduplication, leases, retry ceilings, and “already shown” state.

Each occurrence reads a snapshot of rule counters, keyed by rule identity and session ([OAR-FIRE-4], [OAR-FIRE-5]). Firing enforce rules request increments or resets through `on_fire`: breakers write the core `breaker_count`, counters the host fact `paintedwolf.fire_count`. Monitored, nonmatching, and errored rules apply no side-effects. Host retry observations are separate from these counters.

`WEAK_CREDENTIAL_LITERAL` and `CREDENTIAL_LITERAL` own the credential advisory thresholds. Each declares `x-paintedwolf-counter-scope: paintedwolf.credential_fingerprint` and tests `paintedwolf.scoped_fire_count == 0`, so it warns once per credential per session. Harvest membership is checked against the root session. The host carries no separate warning latch.

## MCP facts (generic)

MCP payloads vary by provider, so device-only bindings project registered call/result fields into a small generic fact set: provider and tool identity, call success, declared structured error code, bounded subject or category, resource identity from a typed field, and readiness of a named MCP requirement.

Bindings read fields, not prose. An error code must come from the declared error structure; success comes from the protocol result. The binding schema validates paths and value vocabularies before admission. These facts support policy composition without embedding provider payload schemas in the rule engine; they do not make the tool available, grant it permission, or claim an external effect the host did not observe.

## Content-safety surface

Inbound content safety uses explicit authority and provenance facts. A rule may observe that content came from a tool, repository, attachment, or remote response and ask for the host's bounded handling effect. It cannot scan the content to decide whether it “looks like instructions.”

Outbound secret protection uses host secret evidence and typed redaction/exfiltration facts. Detector rules can add provenance such as credential minting, but the security subsystem performs screening and approval.

## Pack composition

OAR documents are extension units. Inventory derives identity from each document's namespace and id. Before resolved selection can hide a document, duplicate identities and mandatory disabling are rejected. The production loader validates every admitted document against the declared fact environment and links cross-pack references. The immutable rule set is bound to the contribution frame revision.

Project policy is repository-controlled capability content. Once its exact content is approved, enabled tighten-only policy applies within the allowed project effect ceiling. It cannot define new facts, anchors, effects, or device authority, and an edit invalidates the approved stamp before the new rules can apply.

## Authoring

1. Identify the host transition responsible for the desired behavior.
2. Choose an existing anchor whose frozen facts prove the condition.
3. Express observations neutrally and compose the policy in `when`.
4. Choose the narrowest legal effect and stable code.
5. Put explanatory sentences in catalog templates, not Go.
6. Add positive, negative, and near-miss scenarios.
7. Run rule validation and render the matched guidance.

If the required observation does not exist, add one host fact at the enforcement boundary. Do not approximate it with a keyword, filename, command name, or model claim.

## Conformance

The vendored corpus lives in [`schemas/oar/conformance/`](../schemas/oar/conformance) and runs through the production loader and evaluator ([`corpus.go`](../lycaon/internal/oar/corpus.go), [`cmd/oar-conformance`](../lycaon/cmd/oar-conformance)). A fixture carries `rule` (one document or a list), optional `hints`, and an `input` with `anchor`, `facts`, `recent_tools`, `session_id`, `host_facts`, and `profile`; `expected` asserts any of `decision`, `code`, `on_fire`, `applied`, `counters`, and `error`, and any other key is refused. Each fixture runs on an engine whose anchor profile map is the fixture's own or maps every core anchor to itself, claims both standard profiles, and provides the three reserved detectors.

Pack fixtures use the same shape and run through `pw rules test`; the [portable pack](../lycaon/config/oar-portable-pack) carries the standard's example rules and fixtures.

## Invariants

- Rules observe host facts and request bounded effects.
- Conditions are pure; observation providers and declared side effects have explicit error handling.
- Prose is rendered from catalogs and never parsed back into policy.
- State belongs to the engine or domain subsystem, not the rule.
- MCP bindings project typed fields and grant nothing.
- Project rules cannot widen device authority.
- Stable codes and ids carry meaning; display copy does not.
