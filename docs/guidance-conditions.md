# Guidance conditions

Hint `when` expressions use the **OAR condition language over typed facts**. This page is the shipped DSL reference; the rule document model is normative at <https://openagentrules.org/spec/1.0/>, and this engine's implementation notes are in [Open Agent Rules](open-agent-rules.md).

**See also:** [Open Agent Rules](open-agent-rules.md) · [Agent tool feedback](agent-tool-feedback.md) · [Authorization](authorization.md) · [Security](security.md)

**Machine truth:** stock pack `policy/` dirs under [`lycaon/config/packs/painted-wolf/`](../lycaon/config/packs/painted-wolf) · [`schemas/oar/oar.schema.json`](../schemas/oar/oar.schema.json) · expression parser, type checker, and builtins in [`lycaon/internal/oarcore/`](../lycaon/internal/oarcore) · host fact catalogue and engine in [`lycaon/internal/oar/`](../lycaon/internal/oar) · registry loader in `lycaon/internal/hintregistry/`

---

## Purpose

Every hint emitted on a `guard:*` or `rule:*` channel is an OAR rule binding. The host assembles a typed `GuardContext`, evaluates `when:`, and renders `effect` (`block` / `warn` / `nudge` / `allow` / `transform`) plus optional `on_fire`.

Adding a hint is normally one YAML file. New Go is required only for a new fact provider (observation) or declared `on_fire` effect, never a new predicate language.

---

## Syntax

Expressions use the frozen OAR grammar and closed type environment. Identifiers are fact names from the [fact catalogue](open-agent-rules.md#fact-catalogue). Common shapes:

```yaml
when: session_posture == "spec" && paintedwolf.tool_is_state
when: path_outside_scope("read")
when: !paintedwolf.stub_valid && tool == "task"
when: is_directory
when: paintedwolf.habit_redirect_match == "USE_GREP_REGEX"
```

### Two fact tiers

| Tier | Naming | Examples |
|------|--------|----------|
| **Standard** (portable profiles `tool`, `session`, `filesystem`, `content-provenance`, `content`, `secrets`, `mcp`; [`catalogue.go`](../lycaon/internal/oar/catalogue.go)) | Bare name | `tool`, `session_posture`, `path_outside_scope("read")`, `is_directory`, `not_found`, `path_denied` |
| **Host** | Published as `paintedwolf.<name>` (`HostFactNamespace` in [`oarcopy/facts.go`](../lycaon/internal/oarcopy/facts.go)) | `paintedwolf.surface`, `paintedwolf.stub_valid`, `paintedwolf.unobserved_cited_handles` |

The prefix keeps a portable rule from binding this engine's host vocabulary.

Prefer composing observation facts over echoing a validation code. `when: '"<token>" in arg_validation_errors'` is valid for a **schema** rule that attaches copy to one argument rejection; policy and invariant rules compose the underlying observations. The list carries the observation token a reject publishes, not its `Code:`. The `rejectCodeObservation` map in [`tools/observation_facts.go`](../lycaon/internal/tools/observation_facts.go) resolves a code to its token, and a rule that spells the token itself gates on a fact nothing sets.

| Construct | Notes |
|-----------|--------|
| Identifiers | Declared fact names; dotted host names are single identifiers |
| Calls | Declared observation functions plus `size`, `starts_with`, `ends_with`, and `contains` |
| Logic | `&&` `\|\|` `!` |
| Comparison | `==` `!=` `<` `<=` `>` `>=` `in` |
| Values | Typed literals, list literals, arithmetic, indexing, and ternary expressions |
| Grouping | Parentheses |

The loader parses and type-checks every condition against the closed catalogue. Runtime errors, such as an out-of-range index, follow the rule's `on_error` policy.

---

## Fact discipline

Facts are observations, not verdicts: `paintedwolf.unobserved_cited_paths`, `paintedwolf.workers_idle`, `paintedwolf.command_not_argv`. Composition belongs in `when`. Contracts: `TestOARNoVerdictFacts` and `TestOARObservationOnlyFacts` in [`oar_catalog_contract_test.go`](../lycaon/test/contract/catalogs/oar_catalog_contract_test.go).

---

## Rule document fields

[`oar.schema.json`](../schemas/oar/oar.schema.json) closes the world: `additionalProperties: false`, with one escape hatch for `^x-` implementation extensions. A key that is not below and does not start with `x-` fails validation.

| Field | Role |
|-------|------|
| `oar` | Format marker and compatibility gate (`"1.0"`); its presence is what makes a document a rule. **Required** |
| `id` / stem | Stable `Code:` (= file stem). **Required** |
| `kind` | `schema` \| `policy` \| `invariant` \| `detector`. **Required** |
| `anchor` | Catalog Anchor id ([catalog.yaml](../lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml)). **Required**, and it is `anchor`, never `on`; see below |
| `effect` | `block` \| `warn` \| `nudge` \| `allow` \| `transform`. **Required** |
| `selector` | Optional AND clauses (`profiles`, `tool`, …) |
| `when` | Condition over facts |
| `on_fire` | Declared engine side-effects (counters, breakers, …) |
| `copy` | Nested block holding `title` / `what` / `why` / `cause` / `fix` / `instead` (`instead` is the branch instruction). These are not top-level keys |
| `x-paintedwolf-emit` | Provenance channel (`guard:…`, `rule:…`) |
| `x-paintedwolf-scenarios` | Samples for conformance seeding |

`emit`, `message`, `category`, and `scenarios` are host concerns the portable format does not define, so they ship as `x-paintedwolf-*` extensions (`x-paintedwolf-emit`, `x-paintedwolf-message`, `x-paintedwolf-category`, `x-paintedwolf-scenarios`, plus `x-paintedwolf-audience`, `x-paintedwolf-evidence`, and `x-paintedwolf-severity` where used). Full portable field table: <https://openagentrules.org/spec/1.0/> § Document model.

### `anchor:` on rules, `on:` on bindings

They are different documents, and there is no alias.

| Document | Key | Schema |
|----------|-----|--------|
| OAR **rule** (`policy/<CODE>.yaml`) | `anchor:` | [`oar.schema.json`](../schemas/oar/oar.schema.json) requires `anchor` and declares no `on` |
| Anchor **binding** (`platform/host/bindings/*.yaml`) | `'on':` (quoted, so YAML does not read it as a boolean) | [`anchor-binding.schema.json`](../schemas/anchor-binding.schema.json) requires `on`, `selector`, `effect` |

Both name a catalog Anchor id; the rule asks *when do I evaluate*, the binding asks *when do I render*.

### Flat copy units

Some units under the pack `policy/` dirs are not OAR rule documents: they carry no `oar:` key and no `anchor:`, and hold `emit`, `message`, `what`/`cause`/`why`/`fix`/`instead` flat at the top level. They are informational copy for a code the host raises itself, and `emit` names where that copy is drawn:

| `emit` | Drawn |
|--------|-------|
| `banner` | Appended to the tool result whose feedback raised the code |
| `ui:<target>` | By Den on the named surface |
| `inject:<name>` | Inside the prompt block of the inform anchor `inject.<name>`, such as the active-workflow inject's guidance hints |

None are conditions; nothing evaluates a `when` for them, and they need no rejection fields or scenarios ([`guidance.CopyOnlyEmit`](../lycaon/internal/guidance/reject_code_loader.go)). A rejection a tool returns is an OAR rule on `tool.rejected`, even when the same code also lists in an inject. Author a new conditional hint as an OAR rule document.

`context_schema` (required template keys) belongs to that flat shape and to the user-notice units under `platform/host/user-notices/`. It is not an OAR rule field and the rule schema rejects it.

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
| Every `guard:`/`rule:` hint has `anchor:` + `kind` and loads as OAR | [`oar_catalog_contract_test.go`](../lycaon/test/contract/catalogs/oar_catalog_contract_test.go) |
| Every `anchor:` resolves to a catalog Anchor | same (`ResolveRuleAnchor` + `anchorcatalog.Has`) |
| Emission sites use registry codes | [`guidance_emission_contract_test.go`](../lycaon/test/contract/architecture/guidance_emission_contract_test.go) |
| `when` type-checks against the fact env | OAR loader / engine |

Vendored copies of the portable standard's schemas live in [`schemas/oar/`](../schemas/oar).
