# Example packs

One small, realistic pack per thing an extension can change. Each is a complete
answer to "what would I actually write to do X?" — short enough to read in a
minute, real enough to install.

`test/wiring/installed_pack_reach_test.go` installs every example through the
same `install → resolve → load` path as **Settings → Extensions** and verifies
that each unit reaches its engine.

| Pack | Answers | Unit kinds |
|---|---|---|
| [`house-rules`](house-rules) | "Stop agents doing X in this repo" | `approvals/rules/` |
| [`docs-writer`](docs-writer) | "Add a worker that can only do Y" | `agents/`, `agents/prompts/` (incl. `_persona-contract.yaml`), `tools/profiles/` |
| [`tool-voice`](tool-voice) | "Re-describe one tool for the model" | `tools/schemas/` |
| [`team-flow`](team-flow) | "Give the team our own workflow" | `workflows/`, `workflows/_topologies/` |
| [`tracker-facts`](tracker-facts) | "Make an MCP provider's JSON legible to rules" | `mcp_bindings/` |
| [`credential-recognition`](credential-recognition) | "Recognize our service's credential names and arguments" | `host/credential-slots/` |
| [`onboarding-copy`](onboarding-copy) | "Change what the model reads, and what the human reads" | `guidance/`, `shared/partials/`, `host/bindings/`, `approvals/`, `host/user-notices/` |
| [`editor-surface`](editor-surface) | "Add typed commands, interactions, explicit search, shared operations, and an editor action" | every `contributions/` kind |

A fuller worked set — including the collision, profile, and meta-pack shapes —
lives in the extensions demo repo. These examples cover the loaders and the whole
contribution language; `TestExamplePacksCoverEveryContributionKind` fails if a
kind loses its worked example.

## The rules they all follow

- `id` is `example/<name>`; pack ids must be plain segments.
- `manifest_version: 1`, canonical `version`, and a
  `compatibility.extension_api` SemVer range are required.
- `dependencies.painted-wolf/platform.version` admits the shipped platform
  release; a candidate without a contributing platform is rejected.
- Packs never ship MCP providers, scanner engines, a pack-local `remove:`, or a
  patch onto a stock body.
