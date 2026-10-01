# Policy rule contract

Maintained sources of truth: the Open Agent Rules specification and schema referenced by `docs/open-agent-rules.md`, plus the host capability document and anchor catalogue documented there. `docs/extend.md` defines extension pack placement and validation.

## Stable authoring shape

```yaml
oar: "1.0"
id: EXAMPLE_RULE
kind: policy
anchor: tool.pre_invoke
selector:
  tool: [write]
requires:
  profiles: [session, tool]
when: session_posture == "spec"
effect: block
copy:
  title: Example rule
  what: Writes are refused in spec posture.
  cause: The observed machine condition matched.
  fix: Take the supported alternative.
  instead: Describe the change in the spec instead.
x-paintedwolf-emit: rule:example
```

Place the document directly under `policy/` (subdirectories are not policy units). Identity is `namespace/id`, or the bare `id` without a namespace, not the filename; still name the file `<id>.yaml` so a single-file `pw rules test` selects it. Fields come from the current schema.

New identities may use any implemented anchor. Duplicate qualified identities are refused across all contributing packs; pack ownership cannot replace a rule or disable a mandatory rule. Pre-invoke and handler blocks prevent dispatch. `tool.rejected` observes an intrinsic typed failure before feedback delivery. Transforms are accepted only at boundaries that can deliver rewritten content.

Every non-core fact or profile reference belongs in `requires`. Missing capabilities fail at load; a rule must not treat absence as permission.

## Portability

Prefer the eight OAR core anchors and core facts for portable rules. A documented host anchor, fact, profile, or `x-paintedwolf-*` field is legal only when host-specific behavior is intentional. Presentation copy cannot substitute for selector or condition logic.

Do not add a natural-language classifier, program-name floor, cloud-action list in Go, or an `allow` rule that clears containment or structured boundary authority. Detection overlays remain additive.

## Conformance fixtures

Place JSON fixtures at `<pack>/conformance/*.json`, sibling to `<pack>/policy/` (one object or an array per file). Each fixture carries a JSON copy of the rule document in `rule` (the runner evaluates that copy, so keep it identical to the YAML), an observation `input` with `anchor` and `facts`, and `expected` with `decision` (`block`, `warn`, `nudge`, or `none`), plus `code` or `on_fire` when relevant. Include firing and non-firing cases for the boundary material to the rule.

Run:

```text
pw rules test <pack>/policy --json
```

| Code | Meaning |
|---|---|
| `schema_invalid` | The document violates the OAR schema |
| `condition_invalid` | `when` is invalid against the declared capability catalogue |
| `scenario_mismatch` | A conformance fixture produced another outcome, or an `x-paintedwolf-scenarios` render missed an `expect_contains` string |
| `load_error` | The rule or referenced host capability could not load |

The command is read-only. It exits 1 when a rule fails and 2 when the run itself cannot proceed, such as unreadable fixture JSON. Test success covers checked documents and discovered fixtures only.
