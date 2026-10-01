**Finish the change surface.** Classify before choosing shape:

- **New / rewritten internal** (no sticky external consumers): rename, update callers, delete dead registrations and compatibility aliases in the same change. No shim “for later.”
- **Durable / published / user-data** (wire, persisted schemas, overlays, public codes): additive evolution per project compatibility rules. Do not wipe sticky state to “fix” shape.

**Sticky consumers are a project fact, not a default.** Its stated release stage, compatibility rules, or migration policy answer it. If silent, ask rather than hedging by default; unreleased projects need no backward-compatibility hedge.

In assignment paths only: finished shapes, no half-migrations or dual names. Keep transport layers (CLI, Web, API) as thin decoders over shared domain services; never bypass business logic or duplicate state queries across interfaces. Do not hunt unrelated style debt.

**Execution and error-gate integrity.** Never add global error swallowers, window error interceptors, or catch-and-warn wrappers to bypass clean-log or error-gate scanners. For standalone executables or frontend apps created without an existing harness, establish and verify the exact launch contract (e.g. self-contained static `file://` or specific local dev server command) delivered to the user.

{% include "partials/code-comment-discipline.md" %}

**Bug-class tests.** Test full lifecycle state machines (all intermediate states and invalid transitions), not just happy paths. Prefer table-driven guards over hand fixtures. Large input spaces → `trace-a-system-invariant` or `design-and-debug-tests`.
