# Lycaon — Go backend agent policy

Package-specific rules for the Go sidecar (`lycaon/`). Universal policy: [`../AGENTS.md`](../AGENTS.md). Standard semantics: [`../docs/agents-md-standard.md`](../docs/agents-md-standard.md).

**Run all commands from the repo root** — never `cd lycaon` and invoke bare `go test`.

## Tools (envelope + host-coupled)

- **Envelope-only / safe-command:** implement first-party tools as Go handlers under `internal/tools/native` or one of its family subpackages ([package layering](../docs/package-layering.md#native-tool-family-subpackages)) and consume `internal/tools/safecmd`; external tools use confined MCP. See [`../docs/tools.md` § Safe-command envelope](../docs/tools.md#safe-command-envelope) and [`../docs/dev-tasks.md` § Add a tool](../docs/dev-tasks.md#add-a-tool). Do not re-implement confinement/caps/path/shape/reject outside `internal/tools/safecmd`.
- **Host-coupled** (`read` / `write` / `edit` / `command`): remain Go handlers but own the additional live host dependencies documented in the tool checklist.
- **Workspace mutations go through the write door** — `internal/tools/native/source_write.go` (`applyAgentFile` / `applyAgentStream` / `removeAgentPath` / `renameAgentPath`). Applying and recording are one call: a tool that writes without recording leaves the change unattributed and the watcher logs it as external. Never call `fseffect.Replace`, `os.Rename`, `os.Remove` or `os.WriteFile` elsewhere in that package — `source_write_contract_test.go` scans for it.
- **Durable files elsewhere replace through `internal/fseffect`** — it is the only atomic-replace implementation in the repo. `test/contract/host/one_write_door_contract_test.go` fails any other `os.Rename`, and any `os.WriteFile` under a governed tree; a genuinely different primitive (directory promotion, exclusive publish, stream-then-name content addressing) goes in `doorExemptions` with the reason.
- Guidance timing is Anchor+OAR (`policy/<CODE>.yaml`); command-habit parser observations render through OAR `USE_*_NATIVE` on `tool.rejected` — no new Go kick.

## Commands

```bash
./task check-fast
./task test:digest -- ./internal/foo/...
./task test:failed
./task test:short
./task test:wiring
./task test:contract
./task test:security
./task openapi:bundle
./task codegen:den-types
./task codegen:web-research-catalog
```

Never `go test ./... | tail` or `| grep FAIL` — use digest targets. Full catalog: [`../docs/dev-tasks.md`](../docs/dev-tasks.md).

Durable store baselines: [`../docs/compatibility.md`](../docs/compatibility.md) · [`../docs/sql-persistence.md`](../docs/sql-persistence.md).

## OpenAPI and wire sync

OpenAPI is the source of truth for wire types and operations. Den wire types, ordinary Go DTOs, and operation bindings are generated. The [API conventions](../docs/host-contract.md#11-api-conventions) apply to every `/v1` route.

1. Edit modular sources under `docs/openapi/**`.
2. Run `./task openapi:bundle`.
3. Run `./task codegen:den-types` (writes Den wire types, Go DTOs, and operation bindings).
4. Update handwritten `lycaon/pkg/api/` behavior only when a custom union or JSON implementation changes. Ordinary object schemas use `x-go-generate: true`; Go representation annotations are documented in [`../docs/dev-tasks.md`](../docs/dev-tasks.md#generated-surface-write-paths).
5. Register each `/v1` handler with its generated operation through `Server.registerV1Operation`. This keeps authorization, rate limits, and person-action recording on one route seam. Non-read operations declare `x-person-action: record` or `none` beside `operationId`; reads omit it.

Do those steps in the **same** change. Contract tests in `test/contract/wire/` (`openapi_dto_sync_test.go`, `enum_sync_test.go`, `openapi_routes_sync_test.go`) and `codegen:den-types:check` enforce generated parity. `internal/api/operations_contract_test.go` checks that each generated operation is registered exactly once.

Never edit `docs/openapi.yaml`, `lycaon-den/src/api/types.ts`, `lycaon-den/src/api/operations.generated.ts`, `lycaon/pkg/api/types.generated.go`, or `lycaon/internal/api/operations.generated.go` directly — all are generated.

**Web research provider ids:** author only in `config/packs/painted-wolf/web-research/host/web-research-providers.yaml`, then `./task codegen:web-research-catalog` (writes OpenAPI enum fragment, `pkg/api/webresearch_provider_ids.generated.go`, and Den catalog TS). Do not hand-edit `WebSearchProvider` lists. Still add the Go adapter + `RegisterCatalogProviders` entry.

## Testing

| Layer | Path |
|-------|------|
| Unit | `internal/<pkg>/*_test.go` |
| Integration | `*_integration_test.go`, `test/wiring/` |
| Contract | `test/contract/<domain>/`, with shared support under `test/contract/internal/` |
| E2E / security | `test/security/` |
| OpenAPI runtime | `test/openapi/` — do not import under `internal/` |

Label returned errors: `testutil.FailErr(t, "<step>", err)` — never bare `t.Fatal(err)`. Contract suites use `contractcheck.FailErr` from `test/contract/internal/check`.

No real LLM calls — fixtures and `LYCAON_LLM_MOCK=1` only.

Submit verification runs sequentially through the shared queue described in
[`../AGENTS.md`](../AGENTS.md#testing). Large source-tree correctness fixtures use the
`stress` build tag and `./task test:stress`; routine tests retain representative
pagination and nested-deletion coverage. `test:full` provisions and requires
bundled scanners. Live installed scanner probes require `test:live-scanners`.
Full digest runs use `-count=1` to avoid hashing large filesystem access logs
for the test-result cache. Compilation artifacts remain cached; explicit repeat
counts still apply.

## Coordinator guidance

Never add ad-hoc reject/nudge/kick prose in Go. Use the registry + pongo pipeline:

1. Register `SCREAMING_SNAKE` in `config/packs/painted-wolf/<pack>/policy/<CODE>.yaml` (include scenarios). Optionally `./task codegen:guidance-registry` (stock pack union → `schemas/guidance_registry.json`).
2. Host kicks → template in `config/packs/painted-wolf/platform/guidance/` + `KickEngine`, not a raw `QueuePendingText` call.
3. Guards return `ErrGroundingNudge`; wire via `guidance.FormatCoordinatorNudge` / `StaticRejectFormatter.Format`.
4. Extend `test/contract/architecture/guidance_emission_contract_test.go` — CI rejects inline prose.

Channel map: [`../docs/agent-tool-feedback.md`](../docs/agent-tool-feedback.md). Structured rejects: [`../docs/agent-contract.md`](../docs/agent-contract.md).

## Agent prompt copy (`lycaon/config/`)

Catalog prompts compose per surface via pongo — grep rendered system prompt in debug `llm-requests.jsonl` before editing (`./task den:sidecar:llm-debug`, `./task llm:debug:tail`). Size limits: `sizes` in `config/packs/painted-wolf/platform/host/prompt-budgets.yaml`; `./task budgets` holds prompts to them.

**Generic copy only** — no language-, stack-, or session-specific tuning in shipped catalog defaults. Project-specific behavior belongs in `{project}/.paintedwolf/`, not `lycaon/config/`.

## Structure when limits fire

`funlen` and the size budgets (`./task budgets`) exist so large functions, files, and types get **better pieces**, not quieter checks or higher caps. Root greenfield: [`../AGENTS.md`](../AGENTS.md) § Greenfield policy.

| Do | Do not |
|----|--------|
| Split on a real seam the next reader will look for — a feature file, a phase of a loop, a named decision/result type | Cut a contiguous block into `helper` / `doX` whose only job is to shrink the caller |
| Keep call sites reading as phases or domain steps | Return awkward tuples (`(T, *Result, error)`) just to park an early-exit elsewhere |
| Move types next to the behavior they own | Leave feature types jammed in an unrelated file after the extract |

If the only honest name for the extract is “the rest of this function,” keep editing until the boundary is real.

## Layout

| Path | Role |
|------|------|
| `cmd/`, `internal/` | Sidecar implementation — import layers: [`../docs/package-layering.md`](../docs/package-layering.md) |
| `pkg/api/` | Wire DTOs |
| `config/` | Agents, tools, prompts, hints |
| `test/` | Contract, security, wiring harness |

## Related

- Durable DB / overlay / wire evolution: [`../docs/compatibility.md`](../docs/compatibility.md) · [`../docs/sql-persistence.md`](../docs/sql-persistence.md)
- [`../AGENTS.md`](../AGENTS.md) · [`../lycaon-den/AGENTS.md`](../lycaon-den/AGENTS.md)
- [`../docs/README.md`](../docs/README.md) · [`../docs/architecture.md`](../docs/architecture.md)
- [`../docs/dev-tasks.md`](../docs/dev-tasks.md) · [`../docs/host-contract.md`](../docs/host-contract.md)
- [`../docs/agent-tool-feedback.md`](../docs/agent-tool-feedback.md) · [`../docs/agent-contract.md`](../docs/agent-contract.md)
