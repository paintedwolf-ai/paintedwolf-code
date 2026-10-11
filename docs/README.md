# Painted Wolf Code — technical documentation

This documentation explains the architecture, invariants, and operating model of the product and its local engine. Markdown records the reasoning that keeps those systems coherent; exact wire shapes, generated vocabularies, catalog contents, and executable policy live in machine-readable sources.

Start with [Architecture](architecture.md). It defines the system boundary, the request lifecycle, the core vocabulary, and subsystem owners.

---

## Design principles

| Principle | Consequence |
|-----------|-------------|
| **The host is authoritative for consequential state** | Workflows, tools, permissions, persistence, evidence, and recovery are sidecar decisions. Den renders and requests; it does not reconstruct policy from prose. |
| **One invoked operation has one subsystem owner** | One subsystem contract determines the terminal outcome, evidence, and recovery semantics. Callers do not perform half of another subsystem's operation. |
| **Machine state outranks transcript text** | Workflow progress, authorization, grounding, and closeout use typed facts. Natural-language text teaches the model but does not become a host control signal. |
| **Facts outlive projections** | Durable history and evidence remain authoritative. Model prompts, UI views, indexes, and caches may be compacted or rebuilt without rewriting what happened. |
| **Capability is bounded at the effect** | Attached roots, confinement, mediated egress, and host-authored approvals determine what can happen. Approval is requested when an effect needs authority, not when content is merely encountered. |
| **Parallel work is isolated and reconciled** | Workers receive bounded tasks and private write overlays. The host promotes reviewed results and records what was integrated. |
| **Claims bind to evidence** | Tool receipts, source revisions, findings, verification, and citations let the host distinguish a supported result from plausible prose. |
| **Composition is data** | Workflows, tools, prompts, rules, providers, and extensions are catalogued and validated. New content does not require scattered runtime branches. |
| **Durable surfaces evolve explicitly** | Database baselines, wire, project, and agent-public contracts use direct replacement, versioned formats, generated synchronization, and named deprecations. |

## Reading paths

The pages below are a map, not a linear book. Read the path that matches the change you are making.

### Understand the runtime

1. [Architecture](architecture.md) — processes, subsystem owners, one turn, recovery, and vocabulary.
2. [Projects](projects.md) — project identity, attached roots, and filesystem scope.
3. [Session](session.md) — durable conversation, workflow lineage, checkpoints, and rewind.
4. [Workflows](workflows.md) — declarative phases, gates, choices, and human control.
5. [Coordination](coordination.md) — coordinator/worker responsibilities, per-turn tool loading, and isolated parallel work.
6. [Grounding](grounding.md) — evidence, verification, and supported closeout.
7. [Decision engine](decision-engine.md) — the local model's typed decisions, their limits, and how the heads are retrained.

### Change host or client behavior

1. [Host contract](host-contract.md) — Den/sidecar authority, the host handshake, and reconciliation.
2. [Den](den.md) — client state, workspace structure, and projection rules.
3. [Tools](tools.md) — capability model, execution lifecycle, and worker scope.
4. [SQL persistence](sql-persistence.md) — lifetime parentage, transaction boundaries, and durable effects.
5. [Package layering](package-layering.md) — dependency direction and effect doors.
6. [Test strategy](test-strategy.md) — proof layers and handoff gates.

### Change security or trust

1. [Security](security.md) — threat model, confinement, and trust boundaries.
2. [Authorization](authorization.md) — approvals, grants, leases, and decision evidence.
3. [Secrets and redaction](secrets.md) — protected values, references, screening, durable redaction, and reveal.
4. [Project overlay](project-overlay.md) — project-supplied configuration and trust switches.
5. [Detection packs](detection-packs.md) — additive Sigma asks over the systemic floor.
6. [MCP](mcp.md) — external tool catalogs, definitions, consent, and authentication.
7. [Privacy](privacy.md) — outbound inventory and local data control.

### Extend the system

1. [Extend](extend.md) — pack resolution, contribution kinds, desired state, and publishing.
2. [Workflows](workflows.md) — workflow manifests and reusable primitives.
3. [Adding a tool](adding-tools.md) — native Go and confined MCP choices.
4. [Open Agent Rules](open-agent-rules.md) — typed observations and host effects.
5. [Agent prompt template](agent-prompt-template.md) — anchors, bindings, and prompt composition.
6. [Theme tokens](theme-tokens.md) — the generated theme vocabulary.

### Build, test, and release

1. [Dev tasks](dev-tasks.md) — setup, local stacks, debug capture, and `./task` commands.
2. [Test strategy](test-strategy.md) — test layers and release proof.
3. [Coordinator benchmark](coordinator-benchmark.md) — the graded operation bank, runbook, and calibration.
4. [Compatibility](compatibility.md) — durable-surface classes and the v1 evolution contract.
5. [Dependencies](dependencies.md) — dependency and bundled-toolchain policy.
6. [Dependency inventory](operations/dependency-inventory.md) — update priorities, upgrade constraints, and links to complete inventory sources.
7. [Release operations](operations/release.md) — candidate, publication, update proof, rollout, and recovery.

---

## Page index

| Area | Pages |
|------|-------|
| Core concepts | [Architecture](architecture.md) · [Naming](naming.md) · [Projects](projects.md) · [Project space](project-space.md) · [Project overlay](project-overlay.md) · [Session](session.md) · [Workflows](workflows.md) · [Coordination](coordination.md) · [Decision engine](decision-engine.md) · [Worker result contract](worker-result-contract.md) · [Grounding](grounding.md) · [Prompt assembly](prompt-assembly.md) · [Tools](tools.md) · [Host contract](host-contract.md) · [Den](den.md) · [Compatibility](compatibility.md) |
| Security and trust | [Security](security.md) · [Authorization](authorization.md) · [Secrets and redaction](secrets.md) · [Privacy](privacy.md) · [Detection packs](detection-packs.md) · [Scan supply chain](scan-supply-chain.md) · [Scan findings](scan-findings.md) · [Supported languages](supported-languages.md) |
| Agent/host law | [Agent contract](agent-contract.md) · [Agent tool feedback](agent-tool-feedback.md) · [Open Agent Rules](open-agent-rules.md) · [Guidance conditions](guidance-conditions.md) · [Host behavior](dispatch-hints.md) · [Agent policy standard](agents-md-standard.md) |
| Prompt and input | [Prompt attachments](prompt-attachments.md) · [summarize](summarize.md) · [Search](search.md) · [Asking the user](coordinator-ask-user.md) |
| Files and navigation | [Files stage](files-stage.md) · [Files live layer](files-live.md) · [Source navigation](source-navigation.md) · [In-view find](den-in-view-find.md) · [Git](git.md) |
| Client behavior | [First run](first-run.md) · [Den session switch](den-session-switch.md) · [Den chat items](den-chat-items.md) · [Den notices](den-notices.md) · [Context menus](den-context-menus.md) · [Keyboard shortcuts](keyboard-shortcuts.md) · [Den styling](den-styling-tailwind.md) · [Accessibility](accessibility.md) |
| Models and external tools | [Providers](providers.md) · [MCP](mcp.md) · [HTTP actions](tools.md#http-actions) · [Web research](tools.md#web-research-sources) · [Cost](cost.md) |
| Visual output | [Visual surface](visual-surface.md) · [Theme tokens](theme-tokens.md) |
| Extensions | [Extend](extend.md) · [Adding a tool](adding-tools.md) · [Licensing](licensing.md) · [Trademarks](trademarks.md) |
| Operations | [Dev tasks](dev-tasks.md) · [Test strategy](test-strategy.md) · [Coordinator benchmark](coordinator-benchmark.md) · [SQL persistence](sql-persistence.md) · [Package layering](package-layering.md) · [Dependencies](dependencies.md) · [Dependency inventory](operations/dependency-inventory.md) · [Release operations](operations/release.md) |

## Machine contracts

| Path | Role |
|------|------|
| [`openapi/`](openapi) → generated `openapi.yaml` | HTTP wire |
| [`openapi/vocab/`](openapi/vocab/README.md) | Closed wire vocabularies |
| [`schemas/`](schemas) | Generated SSE event payload schemas and the theme unit schema — codegen output, not hand-written |
| `/schemas/` (repository root) | The schemas compiled at runtime: OAR rules, anchors and bindings, detection rules and packs, host resources, selectors, and the workflow vocabulary |
| `lycaon/config/` | Shipped workflows, tools, prompts, rules, providers, and packs |
| `lycaon/internal/db/schema.sql` | Created database shape |
| `lycaon/pkg/api/` and generated Den types | Co-versioned wire representations |

Markdown does not duplicate a complete machine-readable inventory. It explains the boundary, reason, and consequence, then links to the source that enforces the exact set.

## Documentation standard

Every conceptual page contains, in this order when applicable: the problem it covers; the mental model and why the subsystem exists; enduring invariants and tradeoffs; user- or operator-visible consequences; implementation references and machine truth.

Use diagrams for authority, lifetime, state, and causality, and tables for exact mappings. A diagram uses stable domain nouns rather than function names or routes, and replaces prose rather than repeating it. New top-level pages are for new concept families, not implementation plans or inventories.
