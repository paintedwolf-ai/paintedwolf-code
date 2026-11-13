# A portable rule pack

Seven rules, across five files, that exercise most of Open Agent Rules 1.0 and are portable in
the sense [`[OAR-DOC-23]`](https://openagentrules.org/spec/1.0/) records and section 8
defines: every anchor is a core anchor, and every fact comes from the `core` tier or from a
standard profile the rule names in `requires_facts`. They run unchanged on any engine
providing those facts.

Three of the files carry more than one rule, separated by a YAML `---`. That is a *rule set*,
and a member's position in one has no effect on its identity, ordering, or precedence.

They are written in YAML because that is the comfortable authoring form. JSON is the
interchange form, and the mapping is lossless (`[OAR-DOC-1]`).

| File | What it demonstrates |
|------|----------------------|
| [`read-outside-scope.yaml`](read-outside-scope.yaml) | The basic shape: anchor, selector, condition, effect, fail-closed. |
| [`content-safety.yaml`](content-safety.yaml) | The content-safety surface: a registered detector publishes observations, the rule owns every threshold. Two rules over one detector. |
| [`mcp-server-disabled.yaml`](mcp-server-disabled.yaml) | Facts a host may not provide, declared through `requires_facts`. |
| [`repeat-call.yaml`](repeat-call.yaml) | Escalation over time: the engine counts replayed calls, the rule owns the threshold. |
| [`destructive-tool.yaml`](destructive-tool.yaml) | Deterministic order and short-circuit: the specific refusal subsumes the general flag. |
| [`profile.yaml`](profile.yaml) | The anchor profile map of the host these rules assume. |
| `fixtures/` | Replays against the reference implementation, one per behaviour. |

## The two things worth reading closely

**The threshold is in the rule.** `content-safety.yaml` says `jailbreak_score > 0.8` and
`repeat-call.yaml` says `same_call_run >= 2`. A detector produced the score and the
engine maintains the count; neither decided what was too much. That is the whole argument
of the format — move either number into the host and the policy becomes unreviewable
(`[OAR-FACT-5]`).

**Ordering is the composition.** `destructive-tool.yaml` needs two rules where it looks like
it needs one, and the guaranteed order makes the pair behave: both rules are `policy` kind, so ascending id order evaluates `DELETE_REFUSED` first,
and its block short-circuits the occurrence — a delete is refused outright, and the flag
rule is never reached (`[OAR-EVAL-1]`, `[OAR-EVAL-3]`). `repeat-call.yaml` leans on the same
guarantee from the other side: after one replay the block rule passes and the warn decides.

## Running them

Against the reference implementation:

```bash
node packages/oar-ref/bin/lint-pack.mjs examples/portable-pack
```

That loads every YAML document in the pack, validates it against the published schemas,
checks that the fixtures replay exactly those documents, and replays every fixture in
`fixtures/` against the reference runner. A rule here that fails to load is a
bug in the pack or the engine, not in the specification — report it either way.
