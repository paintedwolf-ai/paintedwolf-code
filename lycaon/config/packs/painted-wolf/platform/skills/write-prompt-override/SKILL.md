---
name: write-prompt-override
description: Create and render-test a project or extension override for an existing prompt template or persona.
---

# Write a prompt override

1. Identify the exact template reference or agent persona and the machine-visible context that should affect it. Render the current target first; do not select a template by matching conversation prose or by editing every shell that happens to contain similar words.
2. Choose one layer. For project behavior, mirror the template reference at `{workspace}/${overlay_dir}/prompt_files/<template-ref>`. For reusable behavior, ship the replacement in an extension pack at the stock unit's path (`partials/x.md` → `shared/partials/x.md`, `agents/x.md` → `agents/prompts/x.md`) and use `own:` to settle the collision.
3. Copy only the structure the override must preserve, then make the smallest coherent template change. Keep permissions, containment, approvals, workflow leaveability, and mandatory grounding outside optional prompt prose.
4. Render a partial with `pw prompts render <template-ref> --project <workspace>`. Supply explicit `--var key=value` values for conditional branches and inspect each capability combination material to the change. `--var` values parse as JSON when valid, and `profile_has_<tool>=true` selects which unit slots render. The preview applies the project's `prompt_files` even when project trust is off; a session applies them only when both trust switches are on.
5. For an agent persona, run `pw prompts render --agent <id> --project <workspace> --check --json`. Branch on render errors or stable `violations` such as `missing_heading:` and `budget_exceeded:`; do not raise a prompt budget to hide unnecessary copy.
6. Validate and reload/link the containing extension when applicable, then start a new turn and inspect the actual rendered system prompt before claiming the override is active.
7. Stop for product design when the request needs a new template variable, host fact, activation matcher, workflow phase, tool, permission, or enforcement rule. A prompt override teaches the model; it does not enforce the host.

See [prompt layering](references/prompt-layering.md) for path mapping, rendering commands, contract checks, and held boundaries.
