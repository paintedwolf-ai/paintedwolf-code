# Naming

Product vs engine names, and one functional id per concept.

**See also:** [Architecture](architecture.md) · [Docs map](README.md) · [Trademarks](trademarks.md)

---

## Product naming (canonical)

| Name | Use |
|------|-----|
| **Painted Wolf Code** | **Product name** — desktop app + local sidecar experience shipped to users |
| **Bialy** | **Tuned local decision engine** — `bialy`; built on Laya, whose model identity and attribution remain upstream |
| **Painted Wolf Code SDK** | **Workflow customization SDK** — `pw workflow validate`, schemas, `.paintedwolf/` authoring |
| **Lycaon** | **Engine codename** — Go module path, `lycaon serve`, and the `lycaon/` · `lycaon-den/` source directories. Not the repository name |
| **`lycaon-den/`** | **Repo directory** for the Tauri frontend — not a user-facing product name |
| **Crossbar** | **Unified search and command overlay** — `Mod+K` quick entry for commands, navigation, and federated search |

The GitHub repository is **`paintedwolf-ai/paintedwolf-code`**. Host config lives under `~/.config/paintedwolf/` (`CONFIG_DIR_NAME`) — a separate leaf from the repo slug.

**Desktop app OS identity** (the bundle id and binary use `painted-wolf`; the host config dir uses the product name):

| Surface | Value |
|---------|--------|
| Product name | **Painted Wolf Code** (`tauri.conf.json` `productName`, window title) |
| Bundle id | **`dev.paintedwolf.code`** — SSOT: `lycaon-den/shared/brand.ts` `BUNDLE_IDENTIFIER` |
| Config dir (release) | **`~/.config/paintedwolf/`** — SSOT: `configdir.DirNameProd` / `CONFIG_DIR_NAME` |
| Config dir (development) | **`~/.config/paintedwolf-dev/`** — SSOT: `configdir.DirNameDev` / `CONFIG_DIR_NAME_DEV` (`LYCAON_DEV=1`, Tauri debug) |
| Main binary | **`painted-wolf-code`** — SSOT: `MAIN_BINARY_NAME` (OS identity; not the config dirname) |
| Engine CLI | **`pw`** — sidecar / terminal CLI in the app bundle (`Contents/Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw`) |
| Capture log CLI | **`pw-logs`** — sibling TUI; `pw logs` execs it |
| Decision engine | **`bialy`** — sibling sidecar serving the local decision model; the host launches it |

Resolution chokepoint: Go [`configdir.UserConfigDir()`](../lycaon/internal/configdir/configdir.go). Den/Tauri and `scripts/config-dir.sh` mirror the same leaf names; Tauri pins `LYCAON_CONFIG_DIR` when it spawns the sidecar. `LYCAON_CONFIG_DIR` as a wholesale override remains tests/harness only.

**Do not** use **"Lycaon Den"** in UI, user-facing docs, or macOS bundle metadata. **Do not** use **“Painted Wolf”** alone for the product or SDK — it is ambiguous. Prefer **Painted Wolf Code** (product) or **Painted Wolf Code SDK** (authoring surface).

### Settings vocabulary

| Term | Use in UI / user docs |
|------|------------------------|
| **AI providers** | Settings + Project configuration section for AI providers and model policy (never bare **Providers** or **Models** for this panel) |
| **MCP providers** | Settings + Project configuration section for Model Context Protocol providers (never bare **MCP** alone for this panel) |
| **Security scanners** | Settings + Project configuration section for the scanner product (never bare **Scanners** or **Security** for this panel) |
| **Extensions** | Settings section label (after Web research, before Cost) |
| **pack** | Unit of install (stock or git) — the thing a person installs, enables, or removes |
| **unit** | One contribution inside a pack — the thing a project may turn off |

Section labels shared by the device Settings list and Project configuration are single constants, so the two surfaces cannot drift: [`settings-nav-model.ts`](../lycaon-den/src/settings/settings-nav-model.ts).

Extensions tabs are the five ids `model`, `packs`, `units`, `settings`, `desired` in [`extensions-tab.ts`](../lycaon-den/src/settings/extensions/extensions-tab.ts), with labels in [`extensions-settings-copy.ts`](../lycaon-den/src/settings/extensions/extensions-settings-copy.ts). Row chips are derived from pack and unit state in [`extensions-chips.ts`](../lycaon-den/src/settings/extensions/extensions-chips.ts), which is where a chip word is added or changed.

Do **not** put the engine name (**Lycaon**) in Extensions chrome or journey copy. Host config examples use `~/.config/paintedwolf/` (release) or `~/.config/paintedwolf-dev/` (development).

## Policy

| Rule | Meaning |
|------|---------|
| **One name** | `delegate_dispatch` everywhere — not a second id in rules with a prefix map |
| **Rename in place** | Change the registry, handler, and YAML together — never add a second name for the same concept |
| **Fail closed** | Contract tests reject forbidden ids in `config/` and rule YAML |
| **No metaphors** | No ethology ids in product surface (`den`, `rally`, `call`, `scout`, `scribe`, `critic` as tool/API names) |

Workflow condition ids are the one naming surface with a machine denylist: [`internal/conditions/forbidden.go`](../lycaon/internal/conditions/forbidden.go) holds exact ids and name patterns that registration refuses, each with a `forbiddenReplacements` entry naming what to use instead, so a rejected id names its successor. Everywhere else the rule is **rename in place**.

## Namespace

| Layer | Field / id examples | Never use as posture |
|-------|---------------------|----------------------|
| **Posture** | `spec`, `build`, `orchestrate`, `vet` | — |
| **Workflow** | `plan`, `options`, `bugbash`, `recon-pack` | ✓ |
| **Phase** | `boot`, `research`, `work`, `done` | ✓ |
| **Topology stage** | `research`, `plan`, `implement` | ✓ |
| **Topology pattern** | `pack` (homogeneous parallel probes) | — |
| **Evidence type** | `security`, `verify`, `plan_review` | ✓ |

Registry: [`session-postures.yaml`](../lycaon/config/packs/painted-wolf/platform/host/session-postures.yaml) maps posture id → default `rules[]`. A posture names a stage and the invoke rules that apply to it; it does not select a tool profile, which comes from the workflow manifest or the session agent. Phase ids are declared per workflow under `phases:` in its `workflow.yaml`, and topology stage names under `pipeline.stages[].name` in `_topologies/*.yaml`. Neither is a fixed global vocabulary, and the layers reuse words — `plan` is a workflow id, a phase id, and a topology stage name, and they are three different things. Always say which layer you mean.

## Blueprints vs Plan vs Artifacts

| Term | Meaning | Do not conflate with |
|------|---------|----------------------|
| **Blueprint** | Governing **`.md` file** under `.paintedwolf/blueprints/**` + `/v1/projects/{id}/blueprints` library | The Plan workflow; Artifacts |
| **Plan** | The `plan` **workflow** | The HTTP library — that is Blueprints |
| **Artifact** | Visual / durable **evidence** media | Governing markdown |

Product copy for the context-zone library is **Blueprints**. Manifest seam key is `blueprint:`. A blueprint is addressed by its **`id`** (UUID), referenced elsewhere as **`blueprint_id`**; a workflow run records the **`blueprint_path`** it binds. SSOT: [`workflows.md`](workflows.md#blueprints).

## “Draft”

“Draft” names three unrelated things, so bare “draft” is ambiguous without context.

| Term | What it names | Where it is defined |
|------|---------------|---------------------|
| **Draft project** | A project whose single primary root is the host-created scratch root (`RootKindDraft`, root label `Draft`). Wire `is_draft` is computed from that root shape, never stored | [`projects.md`](projects.md#draft-project) |
| **Composer draft** | Client-local, unsubmitted chat state on Home. No server project exists yet, and abandoning it leaves nothing behind | [`project-space.md`](project-space.md#chat-first-entry) |
| **Blueprint draft** | A governing Markdown file whose frontmatter carries `status: draft` — written by the agent, not yet approved by a human | [`workflows.md`](workflows.md#blueprints) |

A project's draft status says nothing about any blueprint's approval status, and neither one is the composer's local state.

## What is not a “layer”

- **`pack_*`** — a separate subsystem; not mixed into delegation vocabulary.
- **`.paintedwolf/` paths** — product paths, not metaphor ids.
- **Den** as the frontend directory name — engine-only; product copy says **Painted Wolf Code**.
