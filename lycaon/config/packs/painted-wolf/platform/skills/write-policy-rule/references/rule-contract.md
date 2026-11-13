# Policy rule contract

Maintained sources of truth: the Open Agent Rules specification and schema referenced by `docs/open-agent-rules.md`, plus the host engine declaration and anchor catalogue documented there. `docs/extend.md` defines extension pack placement and validation.

## Stable authoring shape

```yaml
spec_version: "1.0"
id: EXAMPLE_RULE
title: Example rule
kind: policy
on: tool.pre_invoke
selector:
  tools: [command]
  ledger:
    paintedwolf.operation: [write]
requires_facts: [paintedwolf.operation]
when: session_posture == "spec"
effect: block
emit: rule:example
what: Writes are refused in spec posture.
cause: The observed machine condition matched.
fix: Take the supported alternative.
instead: Describe the change in the spec instead.
```

Place the document directly under `policy/` (subdirectories are not policy units). Identity is `namespace/id`, or the bare `id` without a namespace, not the filename; still name the file `<id>.yaml` so a single-file `pw rules test` selects it. Fields come from the current schema.

New identities may use any implemented anchor. Duplicate qualified identities are refused across all contributing packs; pack ownership cannot replace a rule or disable a mandatory rule. Pre-invoke and handler blocks prevent dispatch. `tool.rejected` observes an intrinsic typed failure before feedback delivery. Transforms are accepted only at boundaries that can deliver rewritten content.

Every non-core fact the rule reads in `when`, copy, or a `ledger` clause belongs in `requires_facts`. A fact the host does not provide fails at load; a rule must not treat absence as permission.

## Portability

Prefer the eight OAR core anchors, core facts, and the standard `content-safety` and `mcp` profiles for portable rules. A documented host anchor, `paintedwolf.*` fact, or `x-paintedwolf-*` field is legal only when host-specific behavior is intentional. Presentation copy cannot substitute for selector or condition logic.

Do not add a natural-language classifier, program-name floor, cloud-action list in Go, or an `allow` rule that clears containment or structured boundary authority. Detection overlays remain additive.

## Host extensions

`x-paintedwolf-*` fields are host-specific extensions the loader validates. Two matter most for authoring:

`x-paintedwolf-audience` limits the tool profiles that can receive the rule. Its value is a list of tool profile ids from the host tool profile catalogue, for example `[coordinator]`; every declared id must exist, and an unknown id fails the load. A rule without the field reaches every profile the anchor serves, and the anchor's own audience still applies — a `coordinator.pre_invoke` rule stays coordinator-only even with a wider list.

Feedback copy must not name its own remedies. Bind the host fact `paintedwolf.next_steps`, list it in `requires_facts`, and place a reachable-remedy catalog at `host/next-steps/<CODE>.yaml` beside the pack's policy, one file per rule code. The catalog's top-level `mentions` lists tools the rule's copy names without asking for a call; `steps` entries carry `when`, `tool`, `args`, `program`, `say`, `otherwise`, and `complete`. The host answers each step against the reader's frozen offered surface, replaces an unreachable step with the first reachable step under `or`, then `otherwise`, or drops it, and renders {% verbatim %}`{{ call }}`{% endverbatim %} itself — so a rule's copy must read correctly with the fact empty. `toolvocab.CheckRules` holds bundled copy to this at load: a tool named in copy must be the rule's subject, a declared `mentions` entry, or a next step, and a step must call a current tool with arguments its schema declares.

## Conformance fixtures

Place JSON fixtures at `<pack>/conformance/*.json`, sibling to `<pack>/policy/` (one object or an array per file). Each fixture carries an `id` equal to its file stem, a `covers` list, a JSON copy of the rule document in `rule` (the runner evaluates that copy, so keep it identical to the YAML), an observation `input` with `anchor`, `facts`, and `host_facts` naming each `paintedwolf.*` fact it supplies with its type, and `expected` with any of `decision` (`block`, `warn`, `nudge`, `transform`, or `none`), `code`, `on_fire`, `applied`, `counters`, `copy`, or `error`. Any other `expected` key is refused. Include firing and non-firing cases for the boundary material to the rule.

Run:

```text
pw rules test <pack>/policy --json
```

| Code | Meaning |
|---|---|
| `schema_invalid` | The document violates the OAR schema |
| `condition_invalid` | `when` is invalid against the declared fact environment |
| `scenario_mismatch` | A conformance fixture produced another outcome, or a `scenarios` render missed an `expect_contains` string |
| `load_error` | The rule or a fact it requires could not load |

The command is read-only. It exits 1 when a rule fails and 2 when the run itself cannot proceed, such as unreadable fixture JSON. Test success covers checked documents and discovered fixtures only.
