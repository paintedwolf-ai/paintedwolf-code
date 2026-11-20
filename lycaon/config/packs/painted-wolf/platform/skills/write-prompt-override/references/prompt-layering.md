# Prompt override layering

Maintained sources of truth: `docs/agent-prompt-template.md` (overrides and render preview), `docs/project-overlay.md` (merge order and project trust), and `docs/extend.md` (Resolve and `own`). This recipe does not define a new prompt layer.

## Choose the layer

| Scope | Path | Activation |
|---|---|---|
| One project | `{workspace}/${overlay_dir}/prompt_files/<template-ref>` | Device and project prompt trust switches |
| Reusable extension | Same file at the mapped pack path (`agents/`→`agents/prompts/`, `partials/`→`shared/partials/`, `archetypes/`→`shared/archetypes/`, `guidance/`→`guidance/`) | Pack install/enable plus explicit `own:` for collisions |
| Distribution/site | `prompt_files/` beside the distribution's `config/` (checkout builds only) | Wins over project and bundled layers |

Layer order is site, then project (session folder before primary root), then bundled pack templates. A `units/<stem>.md` override replaces the body only; omit front matter, which stays with the pack unit. In a pack unit's front matter, `attaches` names tools or the reach marks `command_mutation`, `command_external_exec`, and `command_network_egress`; the unit renders while the reader holds any one. Prefer attaching a unit to the reach it needs over wrapping its body in a guard. Do not write an override to an arbitrary Markdown path; mirror the complete reference, including directories and extension.

## Use the bounded syntax and declared reader vocabulary

Prompt files support interpolation, conditionals, bounded loops, and statically named includes. Dynamic includes, inheritance, macros, imports, stateful operations, random operations, and body-buffering tags fail closed. Source, dependency graph, context, output, and execution are bounded.

Reader facts are the names listed in `platform/host/reader-facts.yaml` (`lycaon/config/packs/painted-wolf/platform/host/reader-facts.yaml` in a source checkout, and listed below for an installed build):

- Explicit facts: `agent_tools`, `caps.vision`, `card_{edit_deferred,may_dispatch,may_edit,may_run,only_tools}`, `command_on_floor`, `command_output_files`, `execution_mode`, `has_git`, `is_coordinator`, `lane_s_sync_tools`, `more_tools_loadable`, `native_edit_limit_mib`, `on_worker_branch`, `profile_command_{external_exec,mutation,network_egress}`, `profile_has_{code_rewrite,managed_secrets,page_controls,retrieval,scan_drilldown,summarize,survey_repo,terminal_capture,write_tools}`, `profile_mutation_capable`, `progress_gated_tools`, `progress_ungated_lifecycle_tools`, `read_globs`, `recall_may_widen`, `reject_codes`, `requestable_capabilities`, `root_count`, `runner_{direct_ip,local_listen,loopback_connect,socket,write_root}_tools`, `surface_card_label`, `surface_card_rule`, `surface_id`, `surface_publishes_text`, `tool_check_{scan_drilldown,survey,verify,write}_tools`, `tool_profile`, `visual_review_cadence`, `visual_show_{available,needs_request,page,terminal,terminal_needs_request}`, `web_search_enabled`, `write_globs`.
- Fact families: `profile_has_<tool>`, `profile_can_request.<tool>`, `mints.<kind>`, `receipt_kind.<tool>`, `agent_has_skill_<skill>`.

An override that reads an undeclared reader name still renders, with that name false, and logs a note naming the template and variable (`prompts: unknown reader fact <var> in <ref>`). A template supplies its own occurrence data through its binding; it cannot redefine a reader fact.

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
