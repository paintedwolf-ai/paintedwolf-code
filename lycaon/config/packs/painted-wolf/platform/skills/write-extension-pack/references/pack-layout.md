# Extension pack layout

Maintained source of truth: `docs/extend.md`. This file is a compact author recipe, not a private API.

## Manifest

```yaml
manifest_version: 1
id: acme/example
name: Example
version: 1.0.0
compatibility:
  extension_api: "^1.0.0"
  requires_capabilities: [approvals]
dependencies:
  painted-wolf/platform:
    version: "^1.0.0"
```

- `manifest_version`, `id`, `name`, canonical SemVer `version`, and `compatibility.extension_api` are required. The document version is currently `1`; the host extension API is currently `1.0.0` and must satisfy the declared range.
- Optional: `compatibility.requires_capabilities`, `dependencies`, `requires_scanners`, and `feature`. Use capability `approvals` when the pack relies on approval-rule support. A non-stock dependency also requires a `source`; unmet dependencies or host-resource requirements mean the pack does not contribute.
- Pack ids use plain segments only — `.`, `..`, and dot-prefixed segments are rejected.

## Unit directories (add only what you need)

| Directory | Unit id pattern |
|---|---|
| `host/credential-slots/` | `host/credential-slots/<pack-id>:<file-stem>`; additive credential recognition, device-only |
| `policy/` | `policy/<namespace>/<id>`, or `policy/<id>` without a namespace |
| `guidance/` | `guidance/<stem>` |
| `host/bindings/` | `host/bindings/<stem>` |
| `mcp_bindings/` | `mcp_bindings/<id>` |
| `tools/schemas/` | `tools/schemas/<name>` |
| `host/user-notices/` | `host/user-notices/<code>` |
| `workflows/<id>/workflow.yaml` | `workflows/<id>` |
| `agents/`, `tools/`, `playbooks/`, `shared/` | per-kind roots |
| `approvals/**/explain.yaml` | approval explanation copy |
| `approvals/rules/<rule-id>.yaml` | `approvals/rules/<rule-id>` |
| `host/detection-packs/<id>/pack.yaml` | `host/detection-packs/<pack-id>:<id>/pack` |
| `host/detection-packs/<id>/rules/<slug>.yml` | `host/detection-packs/<pack-id>:<id>/rules/<slug>` |
| `host/detection-packs/<id>/fixtures.yaml` | `host/detection-packs/<pack-id>:<id>/fixtures` |
| `contributions/<kind>/<name>.yaml` | `contributions/<kind>/<pack-id>:<name>` |
| `skills/<name>/SKILL.md` | `skills/<name>` |
| `profiles/*.yaml` | not units — named desired-state sets |

One unit file defines one unit id. Resolve algebra: `effective = provide(enabled packs) − disabled ⊕ own`. Packs never tombstone other packs’ units.

OAR policy identity comes from the document, independently of its filename. Duplicate qualified identities are rejected before resolve filtering; `own` cannot replace a rule. Mandatory policies cannot be disabled.

Contribution, credential-slot and detection-pack unit ids carry your pack id, so file names are yours to choose: another pack shipping `commands/format.yaml` is a different declaration, not a competing one. Those units are never `own`-able, because there is only ever one contribution.

Credential-slot units follow the [recognition contract](credential-slots.md). They add names and payload mappings to the bundled baseline; OAR owns advisory effects. Declare capability `host.credential_slots`.

A detection pack under `host/detection-packs/` carries Sigma rules that add one approval ask or hold a mediated CONNECT. Additive at every scope: a pack id already provided is refused — the first claimant keeps it and stays live — `own:` is refused for these units outright, every unit must come from the pack providing its manifest, and a project may contribute none. Ship `fixtures.yaml` — `pw extensions validate` rehearses each declared case through the production adapters, and fails a rule that falls outside the supported Sigma subset, since nothing it declares would run. Use the `write-detection-pack` skill for rule authoring.

An approval rule file contains exactly `category`, `pattern`, and `effect`. Categories are `tool`, `command`, `mcp`, `path`, `host`, `write_root`, and `host_resource`; effects are only `ask` and `deny`. Unknown fields or an allow/grant/posture/reuse spelling are invalid. A project pack may add a new approval-rule id but cannot collide with, disable, or own a device approval unit; any deny in either layer outranks asks.

## Forbidden in a pack

- MCP provider bodies (`command` / `url` / spawn)
- Scanner engine definitions or binaries
- Pack-local `remove:` semantics
- Markdown patch/merge onto stock bodies
- Self-grant of agent breadth, approval bypass, or containment exceptions

MCP providers and scanners stay in Settings / user catalogs. A pack refers to scanners via `requires_scanners`, and to MCP through `contributions/mcp-requirements/` units naming an exact provider and required tools.

## Own versus additive

| Journey | Pattern |
|---|---|
| Additive | Ship new unit ids only; install + enable + validate |
| Swap | Fork a thin leaf and apply a profile that disables the stock pack, or set `own` on colliding ids |
| Surgical own | `own` one colliding unit id to the fork; leave the rest of stock effective. Contribution and detection-pack ids cannot collide, so they are never the answer here |

Do not infer collision mode from task prose — ask the human.

## Author loop commands

```bash
./task extensions -- install path:<pack-dir> [--project DIR|--device]
./task extensions -- reload <pack-id>
./task extensions:validate [--project DIR] [--json]
./task extensions -- apply-profile <pack-id> <profile-name> …
./task extensions -- disable|enable <pack-id|unit-id> …
./task extensions -- own <unit-id> <pack-id> …
```

Linked-folder (`kind: path`) installs do not copy bodies into the cache. After edits: **Reload from disk** in Settings → Extensions, or `./task extensions -- reload <pack-id>`. Inspect effective units and diagnostics before declaring the contribution active. Open sessions may need a new turn after reload.

Optional read-only verify verbs in the shipped app: `pw rules test` and `pw prompts render`.
