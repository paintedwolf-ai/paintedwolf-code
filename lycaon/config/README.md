# Bundled config — extension packs

## This tree is a value, not a place

Everything here is compiled into the binary by the `config` package (`//go:embed`). A release does not read a config directory beside the executable.

- **Address it with `config.Rel`, never a host path.** `config.Read(config.Providers)` — not `filepath.Join(root, "config", …)`. `Rel` is a distinct string type, so handing a bundled path to `filepath.Join` is a compile error rather than a read that silently finds nothing in a release. Layout constants live in [`paths.go`](paths.go); add one there rather than spelling a path at a call site.
- **User config is separate.** Domain packages read the device config directory and project overlays. Loaders read bundled values through `config` and overlays through `os`.
- **Tests stage bundled config with [`configtest`](configtest/), not a temp directory.** `configtest.Overlay` replaces named files over the real tree; `configtest.Only` gives an exact tree for fail-closed assertions. Writing YAML into `t.TempDir()` does not change what a loader reads.
- **`LYCAON_CONFIG_ROOT`** points bundled reads at a checkout so YAML can be edited without recompiling. It is resolved once at process start, it is for development and self-hosting, and the desktop bundle leaves it unset. `LYCAON_ENGINE_ROOT` is a different thing entirely — the staged payload root for the OpenGrep binary, git toolchain, browser, and JSON schemas — and is never a config layer.

The `schemas/` tree is deliberately *not* embedded: its consumer wants a directory. A release stages it under the engine root; a checkout keeps it beside `lycaon/`.

---

Stock content ships as **extension packs** under `packs/painted-wolf/<leaf>/`. Authoring hub: [`docs/extend.md`](../../docs/extend.md). Community authors use the **shipped** app (Install from folder / Reload) — not this stock tree.

```text
effective = provide(enabled packs) − disabled ⊕ own   # lycaon/internal/extpacks.Resolve
```

`ResolveCatalog` applies device desired state plus trusted project unit disables. Project `suggest` rows remain install proposals. Default (no files) = all stock enabled. Pack order is not an input. A mutation that disables `painted-wolf/platform` is rejected; externally written invalid state boots on the stock catalog.

Installed packs: `{configdir}/extensions/` — CLI `./task extensions -- …` / `./task extensions:validate` ([docs/extend.md](../../docs/extend.md#install-update-and-reload)).

Host resources are declared by the platform catalog at `packs/painted-wolf/platform/host/host-resources.yaml`; device additions live at `{configdir}/host-resources.yaml`. Stable ids, families, guidance, and semantic agent surfaces sit above disjoint macOS/Linux/Windows realizations containing discovery and typed route details. Skills refer to host-resource ids through `metadata.host_resources` and are available only to agents whose effective tool profiles supply the required surfaces. Write agents also receive a target-free presence list of available, host-supported ids. Discovery uses only the closed, non-command probe primitives documented in [`docs/extend.md` § Host resources](../../docs/extend.md#host-resources); the schema is [`schemas/host-resources.schema.json`](../../schemas/host-resources.schema.json).

The stock **suite** membership manifest is `packs/painted-wolf/meta.yaml` (id `painted-wolf/stock`). It is **not** a pack leaf — no `extension.yaml`, no provide dirs — only membership for grouping, status, and Enable suite. Community suites cache under `{configdir}/extensions-meta/`.

---

## Core + feature map

| Kind | Pack id | Role |
|------|---------|------|
| **Platform** | `painted-wolf/platform` | Anchors, bindings, host shells, shared prompts, native tools, workflow registry / `_templates` / `_topologies`, intake/survey |
| **Security** | `painted-wolf/security` | Jail/ledger OAR, sandbox/approvals host YAML (disableable) |
| **Feature** | `painted-wolf/plan` | Plan recipe vertical |
| | `painted-wolf/implement` | Implement + ambient attach |
| | `painted-wolf/options` | Options recipe |
| | `painted-wolf/bugbash` | Bugbash vertical |
| | `painted-wolf/recon-pack` | Recon vertical |
| | `painted-wolf/security-survey` | Security-survey recipe (distinct from security pack) |
| **Capability** | `painted-wolf/web-research` | Web research |
| | `painted-wolf/scan-guidance` | Scan guidance (policy/bindings; engines in runtime) |
| | `painted-wolf/browser` | Browser tools |
| | `painted-wolf/hitl` | HITL surfaces |

Each pack has strict `extension.yaml` format **1**, a canonical SemVer `version`, and a `compatibility.extension_api` range over host API **1.0.0**. Feature/capability packs depend on `painted-wolf/platform` with a SemVer range. Every file under `packs/` belongs to the one pack leaf its path names.

---

## Layout inside a pack

| Dir | Contents |
|-----|----------|
| `host/credential-slots/` | Additive credential recognition from device-admitted packs; [contract](../../docs/secrets.md#credential-slot-extensions) |
| `policy/` | Flat OAR units (`<CODE>.yaml`, no `hint_codes:` wrapper) |
| `guidance/` | Kick + inject templates (flat; no `kicks/` vs `inject/` peers) |
| `host/` | Operator YAML, `anchors/`, `bindings/` |
| `contributions/` | Commands, menus, keybindings, editor actions, search sources, operations, themes, configuration, and MCP requirements |
| `workflows/`, `agents/`, `tools/`, `approvals/`, `playbooks/`, `shared/`, `scanners/` | Intent dirs as needed; `shared/units/` holds loadable instruction units with front matter ([Prompt units](../../docs/extend.md#prompt-units)) |
| `profiles/` | Optional desired-state profiles |

Packs **provide** only — no pack-local `remove:`.

---

## Residual (RuleEngine playlist)

Posture playlist YAML still loads from `platform/host/posture-rules/` for toolpolicy until OAR prompt-path parity. Workflow `rules:` may still list those paths — not an authoring model for extension packs.

## Fixtures

`fixtures/` stays harness-only (not a stock pack).

## Git toolchain pin

`gitengine/pin.yaml` pins the bundled git + git-lfs artifact (fetched by `./task gitengine:fetch` into the Tauri `engine-root/`). Not a pack leaf.

## See also

- [`docs/extend.md`](../../docs/extend.md)
- [`internal/extpacks`](../internal/extpacks/) · [`configdir`](../internal/configdir/configdir.go)
