# Prompt override layering

Maintained sources of truth: `docs/agent-prompt-template.md` (overrides and render preview), `docs/project-overlay.md` (merge order and project trust), and `docs/extend.md` (Resolve and `own`). This recipe does not define a new prompt layer.

## Choose the layer

| Scope | Path | Activation |
|---|---|---|
| One project | `{workspace}/${overlay_dir}/prompt_files/<template-ref>` | Device and project prompt trust switches |
| Reusable extension | Same file at the mapped pack path (`agents/`→`agents/prompts/`, `partials/`→`shared/partials/`, `archetypes/`→`shared/archetypes/`, `guidance/`→`guidance/`) | Pack install/enable plus explicit `own:` for collisions |
| Distribution/site | `prompt_files/` beside the distribution's `config/` (checkout builds only) | Wins over project and bundled layers |

Layer order is site, then project (active root before primary root), then bundled pack templates. A `units/<stem>.md` override replaces the body only; omit front matter, which stays with the pack unit. Do not write an override to an arbitrary Markdown path; mirror the complete reference, including directories and extension.

## Use the bounded syntax

Prompt files support interpolation, conditionals, bounded loops, and statically named includes. Dynamic includes, inheritance, macros, imports, stateful operations, random operations, and body-buffering tags fail closed. Source, dependency graph, context, output, and execution are bounded.

## Render before and after

```text
pw prompts render <template-ref> --project <workspace> --var key=value
pw prompts render --agent <id> --project <workspace> --check --json
```

`--check` applies persona heading, substring, and byte-budget contracts only with `--agent` (without it, it warns and checks nothing); a template ref and `--agent` are mutually exclusive, and byte budgets exist only for worker personas. A bare partial has no persona contract. Render all meaningful branches with explicit variables because an unknown or misspelled variable may render empty.

JSON violations use stable prefixes including `missing_heading:`, `missing_substring:`, and `budget_exceeded:`. A syntax, limit, or missing-ref error fails rather than selecting bundled content.

After extension reload or project-overlay admission, use a new turn and inspect the actual prompt capture when diagnosing activation. Open turns retain the view with which they started.

## Held outside prompts

Do not use prompt prose as the only authority for:

- tool availability, permission, approval, containment, or egress rules;
- workflow phases, gates, or leaveability;
- mandatory evidence/grounding checks;
- automatic activation derived from user or coordinator natural language.

If one of those must change, modify its machine-state surface through a separate product design.
