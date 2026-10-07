# Painted Wolf Code

**Painted Wolf Code** is a local-first AI coding agent: a Tauri desktop app backed by a Go sidecar that is authoritative for sessions, tools, and workflows. The engine and repo codename is **Lycaon** (`lycaon serve`, `lycaon/` paths) — see [naming](docs/naming.md#product-naming-canonical).

- **Local-first** — your code, sessions, and history stay on your machine. The sidecar is the authority for durable state; nothing reconstructs it from prose.
- **Grounded results** — tool receipts, evidence references, and verification let the host distinguish a supported claim from plausible prose. See [grounding](docs/grounding.md).
- **Workflows with human control** — declarative phases, gates, and approval checkpoints, composable from `.paintedwolf/` project configuration. See [workflows](docs/workflows.md).
- **Parallel workers, isolated writes** — workers receive bounded tasks in private overlays; reviewed results are promoted, never merged blindly. See [coordination](docs/coordination.md).
- **Security by construction** — attached roots define read/write scope, egress is mediated, approvals are requested when an effect needs authority. See [security](docs/security.md).
- **Extensible** — first-party Go tools, confined MCP providers, detection packs, and extension packs; composition is data, not runtime branches. See [extend](docs/extend.md).

The public release target is **macOS 13 (Ventura) or later on Apple Silicon**. The same release cadence also produces candidate Linux AppImages for **x86_64 and aarch64** on the Ubuntu 22.04 baseline and a signed Windows x86_64 NSIS installer; candidate artifacts are not linked or offered through the updater. Platform and publication-state SSOT: [`packaging/release-platforms.json`](packaging/release-platforms.json); release details: [`docs/dev-tasks.md`](docs/dev-tasks.md#release--versioning).

## Features

**Sessions that survive everything.** A session is a durable conversation and execution boundary — ordered entries, workflow lineage, evidence, and recovery. Live tokens are a display projection; searchable transcript state waits for settlement. See [session](docs/session.md).

**The host owns consequential state.** Workflows, tools, permissions, persistence, and recovery are sidecar decisions; the desktop app renders and requests, it does not reconstruct policy. Design principles: [docs/README.md](docs/README.md#design-principles).

**Project-scoped trust.** A project is a durable identity over one or more attached roots. `.paintedwolf/` overlays configure guidance, workflows, and rules — commit-worthy configuration, versioned with your code. See [project overlay](docs/project-overlay.md).

**Evidence over assertion.** Every consequential result binds to receipts: source revisions, command outcomes, citations, and verification status recorded in the session ledger. See [grounding](docs/grounding.md).

## Getting started

Painted Wolf Code ships as a desktop app. Releases are published on the [Releases](https://github.com/paintedwolf-ai/paintedwolf-code/releases) page; candidates for Linux and Windows are built per release but not linked or offered through the updater.

1. Download the release for your platform from the [Releases](https://github.com/paintedwolf-ai/paintedwolf-code/releases) page.
2. Launch the app. The Go sidecar starts with it — no separate service to run.
3. Attach a project root and start a session.

Configuration lives under `~/.config/paintedwolf/`. Every `/v1/*` route requires `Authorization: Bearer <token>` — loopback is not a trust boundary here. Boundary details: [docs/security.md](docs/security.md).

## Building from source

Prerequisites: Go, Bun, Node, and Rust with Cargo, at the versions pinned in `lycaon/go.mod`, `.bun-version`, `.node-version`, and `rust-toolchain.toml`; `./task setup-dev` checks them. macOS development also needs Xcode Command Line Tools (`xcode-select --install`). Full prerequisites and toolchain notes: [docs/dev-tasks.md](docs/dev-tasks.md).

```bash
./task setup-dev     # one-time complete toolchain/dependency setup
./task check-fast    # the gate every pull request runs
```

Prerequisites, the two-terminal Den dev loop, debug capture, and the full `./task` catalog: [`docs/dev-tasks.md`](docs/dev-tasks.md). Rules for agents working in this repo: [`AGENTS.md`](AGENTS.md).

## Documentation

Start at the [docs map](docs/README.md) — it indexes everything below and more.

| Path | Role |
|------|------|
| [`lycaon/`](lycaon/) | Go backend (`lycaon serve`) |
| [`lycaon-den/`](lycaon-den/) | Tauri + Solid.js desktop frontend |
| [`docs/architecture.md`](docs/architecture.md) | The two binaries, the one turn loop, [subsystem owners](docs/architecture.md#subsystem-owners) |
| [`docs/security.md`](docs/security.md) | Threat model, confinement, trust boundaries |
| [`docs/workflows.md`](docs/workflows.md) | Workflow phases, gates, human control |
| [`docs/extend.md`](docs/extend.md) | Packs, tool contribution, extension surfaces |
| [`AGENTS.md`](AGENTS.md) | Agent policy and dev commands |

New here: [architecture](docs/architecture.md) covers the two binaries, the one turn loop, and [subsystem owners](docs/architecture.md#subsystem-owners) — one authoritative operation outcome, distinct from invocation receipts, data parentage, and UI state.

## Changelog

Notable changes are tracked in [`CHANGELOG.md`](CHANGELOG.md), following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html) for the host/desktop stream (`VERSION` + `v*` git tags).

## Community

If it has been useful to you, tell a colleague who would use it, or star this repository.

Reports are welcome. There is no support contract — no guaranteed reply, fix, or timeline.

Questions and ideas start in [Discussions](https://github.com/paintedwolf-ai/paintedwolf-code/discussions). Reproducible app bugs go to [Issues](https://github.com/paintedwolf-ai/paintedwolf-code/issues/new/choose) — pick the form that fits. In-bounds agent mistakes have their own form; they are not automatically defects. Security vulnerabilities go to a [private advisory](https://github.com/paintedwolf-ai/paintedwolf-code/security/advisories/new), never a public issue.

[`REPORTING.md`](REPORTING.md) defines the routing and what a useful report contains · [`CONTRIBUTING.md`](CONTRIBUTING.md) defines the pull-request gate, currently collaborator-only while the first release stabilises · [`SECURITY.md`](SECURITY.md) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Licensing

Application code is [Apache-2.0](LICENSE); first-party catalog YAML under `lycaon/config/` (CC-BY-4.0), vendored engines, and the Painted Wolf marks carry their own terms — full map in [`docs/licensing.md`](docs/licensing.md).
