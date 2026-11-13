# Guidance conditions

Hint `when` expressions use the **OAR condition language over typed facts**. This page is the shipped DSL reference; the rule document model is normative at <https://openagentrules.org/spec/1.0/>, and this engine's implementation notes are in [Open Agent Rules](open-agent-rules.md).

**See also:** [Open Agent Rules](open-agent-rules.md) · [Agent tool feedback](agent-tool-feedback.md) · [Authorization](authorization.md) · [Security](security.md)

**Machine truth:** stock pack `policy/` dirs under [`lycaon/config/packs/painted-wolf/`](../lycaon/config/packs/painted-wolf) · [`schemas/oar/oar.schema.json`](../schemas/oar/oar.schema.json) · expression parser, type checker, and builtins in [`lycaon/internal/oarcore/`](../lycaon/internal/oarcore) · host fact catalogue and engine in [`lycaon/internal/oar/`](../lycaon/internal/oar) · registry loader in `lycaon/internal/hintregistry/`

---

## Purpose

Every hint emitted on a `guard:*` or `rule:*` channel is an OAR rule binding. The host assembles a typed `GuardContext`, evaluates `when:`, and renders `effect` (`block` / `warn` / `nudge` / `allow`) plus optional `on_fire`.

Adding a hint is normally one YAML file. New Go is required only for a new fact provider (observation) or declared `on_fire` effect, never a new predicate language.

---

## Syntax

Expressions use the frozen OAR grammar and closed type environment. Identifiers are fact names from the [fact catalogue](open-agent-rules.md#fact-catalogue). Common shapes:

```yaml
when: paintedwolf.session_posture == "spec" && paintedwolf.tool_is_state
when: path_outside_scope
when: !paintedwolf.stub_valid && tool == "task"
when: same_call_run >= 2
when: mcp_result.status == "denied"
```

### Fact tiers

| Tier | Naming | Examples |
|------|--------|----------|
| **Core** (every anchor) | Bare name | `tool`, `tool_args`, `fire_count`, `breaker_count`, `same_code_reject_run`, `same_call_run` |
| **Standard** (profiles `filesystem`, `content-safety`, `mcp`; [`catalogue.go`](../lycaon/internal/oar/catalogue.go)) | Bare name | `is_directory`, `not_found`, `path_outside_scope`, `secret_matches`, `mcp_result` |
| **Host** | Published as `paintedwolf.<name>` (`HostFactNamespace` in [`oarcopy/facts.go`](../lycaon/internal/oarcopy/facts.go)) | `paintedwolf.session_posture`, `paintedwolf.stub_valid`, `paintedwolf.unobserved_cited_handles` |

The prefix keeps a portable rule from binding this engine's host vocabulary.

Prefer composing observation facts over echoing a validation code. `when: '"<token>" in paintedwolf.arg_validation_errors'` is valid for a **schema** rule that attaches copy to one argument rejection; policy and invariant rules compose the underlying observations. The list carries the observation token a reject publishes, not its `Code:`. The `rejectCodeObservation` map in [`toolrejection/observation_facts.go`](../lycaon/internal/toolrejection/observation_facts.go) resolves a code to its token, and a rule that spells the token itself gates on a fact nothing sets.

| Construct | Notes |
|-----------|--------|
| References | Declared fact names, dotted host names included. A dotted name that is not declared selects a string member of the map fact the rest names, such as `tool_args.path` |
| Calls | Declared observation functions, the counter readers `fire_count_of("ID")` and `breaker_count_of("ID")` with a literal rule id, plus `size`, `starts_with`, `ends_with`, and `contains` |
| Logic | `&&` `\|\|` `!` |
| Comparison | `==` `!=` `<` `<=` `>` `>=` between values of one scalar type; `in` against a list |
| Values | Typed literals, non-empty string list literals, arithmetic, and ternary expressions; no indexing |
| Grouping | Parentheses |

The loader parses and type-checks every condition against the closed catalogue, and each refusal cites its `[OAR-EXPR-n]` clause. Runtime errors, such as an absent map member, follow the rule's `on_error` policy.

---

## Fact discipline

Facts are observations, not verdicts: `paintedwolf.unobserved_cited_paths`, `paintedwolf.workers_idle`, `paintedwolf.command_not_argv`. Composition belongs in `when`. Contracts: `TestOARNoVerdictFacts` and `TestOARObservationOnlyFacts` in [`oar_catalog_contract_test.go`](../lycaon/test/contract/catalogs/oar_catalog_contract_test.go).

---

## Rule document fields

[`oar.schema.json`](../schemas/oar/oar.schema.json) closes the world, with one escape hatch for `^x-` implementation extensions. A key that is not below and does not start with `x-` fails validation.

| Field | Role |
|-------|------|
| `spec_version` | Format marker and compatibility gate (`"1.0"`); its presence is what makes a document a rule. **Required** |
| `id` / stem | Stable `Code:` (= file stem). **Required** |
| `kind` | `schema` \| `policy` \| `invariant` \| `detector`, or `hint` for static copy that is never evaluated. **Required** |
| `on` | Catalog anchor id ([catalog.yaml](../lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml)). **Required** |
| `namespace` | Publisher scope; identity is `namespace/id` |
| `selector` | `tools`, the tools the rule applies to; `{}` selects every occurrence. Anything else a rule scopes on is a fact in `when`. **Required** |
| `requires_facts` | Every non-core fact the rule reads in `when` or copy |
| `effect` | `block` \| `warn` \| `nudge` \| `allow`. **Required** |
| `when` | Condition over facts |
| `on_fire` | Declared engine side-effects (counters, breakers, events) |
| `enforcement`, `mandatory`, `on_error` | Monitor or enforce; whether configuration may disable the rule; `fail_closed`, `fail_open`, or the id of a same-namespace rule whose block substitutes |
| `what` / `cause` / `why` / `fix` / `instead` / `message` | Copy, top-level; `instead` is the branch instruction |
Host metadata ships as `x-paintedwolf-*` extensions: `x-paintedwolf-emit` (provenance channel, `guard:…` or `rule:…`), `x-paintedwolf-category`, `x-paintedwolf-scenarios` (copy renders), `x-paintedwolf-evidence`, `x-paintedwolf-context-schema`, `x-paintedwolf-audience`, `x-paintedwolf-status`, and `x-paintedwolf-successor`. See [Open Agent Rules](open-agent-rules.md#host-extensions).

Rules and anchor bindings (`platform/host/bindings/*.yaml`, [`anchor-binding.schema.json`](../schemas/anchor-binding.schema.json)) both name a catalog anchor in `on`; write it `'on':` in YAML so it is not read as a boolean. The rule asks *when do I evaluate*, the binding asks *when do I render*.

### Flat copy units

Some units under the pack `policy/` dirs are not OAR documents: they carry no `spec_version`, and hold `x-paintedwolf-emit`, `message`, `what`/`cause`/`why`/`fix`/`instead` flat at the top level. They are informational copy for a code the host raises itself, and `x-paintedwolf-emit` names where that copy is drawn:

| `x-paintedwolf-emit` | Drawn |
|--------|-------|
| `banner` | Appended to the tool result whose feedback raised the code |
| `ui:<target>` | By Den on the named surface |
| `inject:<name>` | Inside the prompt block of the inform anchor `inject.<name>`, such as the active-workflow inject's guidance hints |

None are conditions; nothing evaluates a `when` for them, and they need no rejection fields or scenarios ([`guidance.CopyOnlyEmit`](../lycaon/internal/guidance/reject_code_loader.go)). A rejection a tool returns is an OAR rule on `tool.rejected`, even when the same code also lists in an inject. Author a new conditional hint as an OAR rule document.

Their copy binds host data the raising code path supplies, which an OAR hint's static copy cannot carry. `x-paintedwolf-context-schema` lists the template keys a flat unit requires; the same field is informational on a rule.

---

## Where a fired code surfaces

A matched rule's code reaches the agent on one of three structured surfaces, deduped by code before attach:

| Surface | Field |
|---------|-------|
| Reject block in a tool-role message | the `Code:` line ([`hostmarker.CodeLine`](../lycaon/internal/hostmarker/hostmarker.go)) |
| Tool result on the wire | `feedback[].code` (`ToolFeedback{code, details, subject}`), projected into `codes[]` for card selection |
| Per-tool JSON envelope | that envelope's `hint_code` field, rendered by [`guidance.EnvelopeHintMessage`](../lycaon/internal/guidance/envelope.go) |

Full branch contract: [`agent-contract.md`](agent-contract.md#core-rule-branch-on-codes-never-interpret).

---

## Registry index

Machine-readable subset: [`schemas/guidance_registry.json`](../schemas/guidance_registry.json), regenerated by `./task codegen:guidance-registry` (`oar.SyncRegistryFromStock` unions every stock pack `policy/` dir; it never rewrites hint YAML).

## Integrity

| Check | Where |
|-------|--------|
| Every `guard:`/`rule:` hint has `on` + `kind` and loads as OAR | [`oar_catalog_contract_test.go`](../lycaon/test/contract/catalogs/oar_catalog_contract_test.go) |
| Every `on` resolves to a catalog anchor | same (`ResolveRuleAnchor` + `anchorcatalog.Has`) |
| Emission sites use registry codes | [`guidance_emission_contract_test.go`](../lycaon/test/contract/architecture/guidance_emission_contract_test.go) |
| `when` type-checks against the fact env | OAR loader / engine |

Vendored copies of the portable standard's schemas live in [`schemas/oar/`](../schemas/oar).
