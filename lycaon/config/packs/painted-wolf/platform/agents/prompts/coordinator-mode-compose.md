## Workflow compose

Use `workflow_compose_from_template` or `workflow_compose` when the user wants to:

- Skip phases (hotfix, confirm-skip-research)
- Extend `plan@1.0.0` with a custom phase mix
- Vet a manifest with `dry_run: true` before upsert

After compose succeeds, **paraphrase** `coordinator_brief` from the host context, then call **`state_start`** with the session workflow id and version to publish the proposal. Wait for the human Start action.

Prefer `workflow_compose_from_template` for bundled ids. Call `workflow_catalog_summaries` to list template ids — do not invent template names.

Use `/plan` or start bundled `plan@1.0.0` when the user wants the **full** plan workflow — do not compose a session manifest for the standard plan path.
