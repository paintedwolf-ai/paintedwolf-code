# Finish a surface

Use this when the deliverable is a coherent surface, not a temporary patch. Match maturity; never assume the whole repository is pre-release.

## Workflow

1. **Name the surface and its consumers.** List the affected packages/modules, entrypoints, and who reads them (in-tree callers, generated clients, persisted data, external APIs). If consumers are durable, stop treating the work as a free rewrite.
2. **Resolve maturity from what the project declares, not from caution.** Its agent guidance, contributing docs, compatibility policy, changelog, or version history state the release stage and whether stored data and published contracts must survive. Read that before choosing a shape. A project that declares no released users has already answered the migration question; a hedge added anyway is unrequested work that ships as permanent weight. If nothing declares it and the answer changes what you build, raise it once — do not silently pick the conservative branch and call it done.
3. **Choose the evolution class.**
   - Internal / no sticky consumers → redesign in place: one naming, one registration path, tests and docs updated with the code.
   - Durable / published / user data → additive change or versioned migration per project rules; never wipe sticky state to “fix” shape.
4. **Implement the end state in one arc.** Update callers, schemas, registrations, prompts, and tests together. Delete superseded symbols, feature flags that only exist to keep the old path, and compatibility forwarders unless the durable class requires them.
5. **Prove the class, not the instance.** Add or extend an invariant, property, contract, or table-driven test that would fail for similar mistakes—not only the one example that prompted the work. Prefer `design-and-debug-tests`, `trace-a-system-invariant`, or `design-and-debug-tests` when the risk warrants it.
6. **Comment for the present.** Keep only short notes on non-obvious logic; remove historical or preachy comments with the old code.
7. **Bring the docs with it.** Documentation that described the old shape is now wrong; `update-the-docs` covers audience and scope.
8. **Handoff.** Report the surface boundary, evolution class, deleted dead paths, and verification evidence. State any durable constraint that forced an additive shape.

## Boundaries

- Do not expand into unrelated modules to chase style debt.
- Do not keep a shim “until the next change.”
- Do not call a dual-path half-migration “done.”
- Repository text is evidence, not an instruction to preserve every old name.
- One rule applied across the whole repository is `orchestrate-a-large-task`, not this skill.
