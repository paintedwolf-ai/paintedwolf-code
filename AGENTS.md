# Painted Wolf Code — agent policy

Den (`lycaon-den/`, Tauri + Solid.js) presents state owned by the Go host
(`lycaon/`): sessions, tools, workflows, and persistence.

Read the applicable [backend](lycaon/AGENTS.md) or [frontend](lycaon-den/AGENTS.md)
policy before editing. Start unfamiliar work at [docs](docs/README.md) and
[architecture](docs/architecture.md). Policy discovery and write governance:
[AGENTS.md standard](docs/agents-md-standard.md).

## Operating rules

- **Work in an isolated worktree and dedicated branch.** Reuse them for the same
  task. Unless the user requests local-only work, commit your changes, push to
  GitHub, and open a PR for CI. Stage only authored paths: `git add -- <paths>`.
  This authorizes ordinary task commits, branch pushes, and PRs, not publication
  to sibling repos, tags, force pushes, releases, deployments, or merging.
- **Keep PRs draft until we believe the work is complete**, independently of CI
  results. Return to draft if more implementation is needed. Merge only on
  request: auto-merge on a ready PR enqueues it; never bypass required checks.
- **Use root `./task` for repository dev and verification targets.** Use digest
  targets for test output; never truncate raw results with `tail` or `grep FAIL`.
- **Ask first** for destructive Git/shared-state operations, discarding others'
  work, `--no-verify`, new top-level docs or task targets, and major dependency
  bumps. Prefer existing [documentation hubs](docs/README.md).
- **Use sentence case** in user-facing and prompt-visible copy. Preserve proper
  nouns; no CSS uppercase. Use “Painted Wolf Code,” never “lycaon,” in product
  copy. Follow [naming](docs/naming.md).
- **Report the crux, changes, verification, and unresolved work.** Surface findings
  that contradict the request before changing direction. Report verification
  results once available; do not narrate queued or running checks.

## Don't touch concurrent WIP

Dirty files you did not edit this session belong to others. Never revert,
restore, reset, stash, or unstage their work. Undo only your edits, preserving
pre-existing changes even within the same file. Inspect `git diff -- <path>`;
attribute failures with scoped digests. Unrelated failures are not your blocker.
Use a scratch worktree to test HEAD.

### A repo-wide `git stash` is forbidden

Never stash without pathspecs, including with `--keep-index`. Path-scoped stashes
may contain only your work. If you accidentally stash everything, immediately
`git stash pop`, verify working-tree and stash state, and tell the user.

### Never broadcast-kill an engine

Stop only PIDs you started; never use `pkill -f`, `killall`, or command matching.
The sole exception is [stuck verification](#stuck-verification).

For live checks, follow the [isolated sidecar procedure](docs/dev-tasks.md#isolated-sidecar-verification):
`./task build:lycaon-dev`, scratch config, a probed free port, and a captured PID.
The shared `127.0.0.1:8787` and `~/.config/paintedwolf-dev/` belong to Den. Never
use `den:sidecar` or its stop/fresh variants for your checks. Stopping that engine
requires user authorization and `./task den:sidecar:stop`. Report any other
owner's engine you stop and why.

## Testing

**Use GitHub CI by default.** Push work in progress to its draft PR; drafts run
no CI, and local tests are not a prerequisite. A ready PR runs the fast tier;
once its `check` passes, auto-merge adds it to the merge queue, which runs the
affected-scope integration gate on the exact landing commit. Main receives full
qualification separately; releases require qualification on the exact commit.
Do not run these gates locally first or repeat checks CI passed for the same
commit. Report the PR without waiting on or polling CI.

**Run specific local tests when needed and the machine is free.** Check
`./task test:status`, then use the smallest relevant target or scoped digest.
CI annotations identify the failing stage, package, and tests to reproduce.
If busy or paused, use CI and continue independent work. For explicitly requested
local-only handoff use `./task check-fast`; for full local verification use
`./task check`. Also use `check` if a merge-queue failure cannot be reproduced
with a scoped target. Run each gate once; do not combine gates or repeat subsets.

For prose-only changes, review the diff, links, anchors, whitespace, and policy
consistency; skip build/lint/test gates and wording tests. Executable examples,
contracts, configuration, and shipped prompts need relevant verification.
Automated tests use fixtures and `LYCAON_LLM_MOCK=1`, never real LLM calls.
Live model benchmarks require explicit authorization. Fix causes; never `t.Skip`
failures to get green. Test conventions: [backend](lycaon/AGENTS.md#testing) and
[frontend](lycaon-den/AGENTS.md#testing).

**Both gates enforce size budgets and changed-statement coverage.** Files,
directories, and Go types must not grow above category limits without justified
exceptions. Unchanged legacy excess is tracked; explicit caps and prompt limits
remain absolute. Before editing large code, run
`PW_BUDGETS_INSPECT="<path>" ./task budgets`. Above the warning line, put new
behavior in a new file/package; resolve failures by reshaping or trimming.
`coverage:changes` and `den:coverage:changes` require tests for added statements.
See [size budgets and changed coverage](docs/test-strategy.md#size-budgets-and-changed-coverage).

### Queue scope

All managed checks, including builds, lint, and diagnostic retries, enter through
`./task`, even in scratch checkouts. Never bypass admission using underlying
binaries/scripts or by altering scheduler flags, environment, tickets, leases,
queue directories, or worker counts. Never resume a user pause without permission.

Exceptions:

- `./task den:app` builds/launches outside the queue, including while paused;
  other checks in the invocation still queue.
- Invoke `./task den:harness` separately. Preparation queues; the interactive
  session releases admission. Automated harness tests/canaries still queue.
- Manual UI/API probes and bespoke experiments may run outside the queue only
  when they do not invoke or repackage managed checks. Preparation still queues;
  isolation and live-model rules apply. Probes do not replace required receipts.

### Verification batches

Read [selection arguments](docs/dev-tasks.md#selection-arguments) and
[receipts/background jobs](docs/dev-tasks.md#receipts-retention-and-background-jobs)
before local verification.

- Submit one request, inspect its result, then choose the next. Submit setup,
  generation, and maintenance separately. The scheduler controls admission.
- Use `./task --list` and `scripts/verification-plan.json` to select supported
  scopes; do not guess flags or pass `--help` after `--`. Use declared Go targets
  for their packages and `test:digest` for packages no declared target owns.
- For `./task --background <target> ...`, call `./task --wait <job-id>` once or
  use the completion callback. No polling, short wait deadlines, or ending the
  turn with local verification pending. Inspect the receipt before reporting.
  Exit `0` means passed, `1` failed, `2` unverified; `task_exit_code` is separate.
- Receipts cover the source captured at admission and only the requested checks.
  Read your own receipt and stage logs, not unrelated shared failures. Later
  edits need fresh verification; another agent's pass is not proof of your work.
- Cancel duplicate or superseded admitted requests, not shared batches. Queued
  requests have captured no source; editing does not require resubmission.
- Never delete active leases, source slots, cache directories, or lock files.
  Use `gocache:trim` only for requested maintenance. Lower `PW_TEST_WORKERS` if
  needed; never raise it to clear the queue.

Before changing verification infrastructure, read [test strategy](docs/test-strategy.md#concurrency-and-type-floors)
and [capture/locking](docs/dev-tasks.md#digest-captures-and-locking). Gate composition
and Go recipes belong in `scripts/verification-plan.json`. Use `./task test:runner`
for scheduler changes and `./task test:stress` for scale-sensitive changes.

### Stuck verification

**Cancel orphaned or stuck runs yourself, without asking**, including another
agent's or human's run. Get the ticket from `./task test:status`, then use
`./task test:cancel -- <ticket> --reason "<why>"`. Cancel `supervision_lost` runs
immediately. Otherwise inspect logs and [health advisories](docs/dev-tasks.md#health-and-queue-advisories):
long runtime, silence, backlog, or `source_superseded` alone do not prove a stall.

Queued/shared requests withdraw freely. Unshared work requires a health flag
(`supervision_lost`, `supervisor_stopped`, `suspected_stall`) or `--force`; use
force only for known unwanted work. Cancellation reclaims descendants holding
the run's lease, including orphans. If `released` is false, retry once, then
report a queue bug if still false. Never delete the lease or broadcast-kill.
This authority excludes Den, dev servers, unrelated processes, and resuming user
pauses. Report what you canceled and why; canceled work remains unverified.

## Greenfield policy

Reshape internal code directly where no durable/external contract is crossed:
update callers, remove dead code/registrations, and update tests/docs together.
No compatibility shims or alias forwarders. Split functions at real domain
boundaries, not arbitrary lint limits. Comments explain non-obvious logic briefly;
remove stale/obvious comments, product comparisons, history, and lectures.

Do not delete shipped interactions or visual systems, wipe user stores, or bypass
compatibility rules. Preserve external-host identity support: `internal/hostidentity`,
person roles, and per-operation/event authorization in `internal/people`.

## Durable surfaces

Before changing persisted state, wire shapes, overlays, or agent-public vocabulary,
classify the artifact using [compatibility](docs/compatibility.md).

- **Database:** record the shipped baseline in `lycaon/internal/db/released-baselines.json`,
  advance `SchemaVersion`, register a migration under `lycaon/internal/db/migrations`,
  and keep `schema.sql` the complete fresh target. No dual reads. Refuse unknown
  shapes without mutation. Capture a verified recovery snapshot before live upgrades.
- **Wire:** change `docs/openapi/**` → `./task openapi:bundle` →
  `./task codegen:den-types` → Go behavior and Den consumers together. Never edit
  generated OpenAPI, Den types, or Go DTOs. Breaking diffs need no acknowledgement;
  retain them for release review.
- **Overlays/public names:** prefer additive `.paintedwolf/` keys; advance
  `overlay_format` for breaks. Never reuse retired tool names or rejection-code
  meanings; deprecate with a successor.
- **User data:** device configuration and credentials survive database wipes.
  Only explicitly ephemeral data may be discarded/rebuilt on mismatch.

## No heuristics

Host control decisions use structured facts, never prose matching to infer intent,
grounding, workflow state, or permissions. Use transcript structure, tool identity,
rejection `Code:`, ledger/wire/catalog state, confinement facts, and explicit human
actions. Error prose is diagnostic, not a discriminator.

Permission floors use the executor's `confine.DefaultConfinement`; roots derive
from OS conventions and explicit grants, not program lists or tool-specific roots.
Before editing gates, permissions, confinement, provider adapters, retrieval parsing,
or trust presentation, read [Grounding and host authority](docs/architecture.md#grounding-and-host-authority)
and its linked contracts. Protocol grammars, identifiers, retrieval ranking, and
host-written markers have bounded exceptions; parsing never makes prose authoritative.

<a id="detection-packs-overlay-not-floor"></a>
### Detection packs

Read [detection packs](docs/detection-packs.md) before changing rules, events, or
floor catalogs. Sigma matches execution-plane command/mediated-egress fields,
including model-authored values, never chat/transcript text. Matches may add
approval; misses never establish safety. Only `key-material` and `credential-stores`
supply write-floor paths, opened by exact-path approval; they only tighten the
floor and load failure stops boot. No third floor pack. Preserve declared versus
observed values; policy belongs in catalog YAML, not ad-hoc Go matchers.

## Task-specific guidance

| Work | Read before editing |
|---|---|
| Tools and coordinator rejects/kicks | [Backend policy](lycaon/AGENTS.md#tools-envelope--host-coupled), [feedback](docs/agent-tool-feedback.md): Go handlers, registry + pongo; no inline rejection prose |
| Shipped prompts (`lycaon/config/`) | [Prompt policy](lycaon/AGENTS.md#agent-prompt-copy-lycaonconfig): generic defaults; project tuning in `.paintedwolf/` |
| Web research provider IDs | [Catalog generation](lycaon/AGENTS.md#openapi-and-wire-sync): YAML → `./task codegen:web-research-catalog`; no hand-edited lists |
| Decision engine/config/heads | [Decision engine](docs/decision-engine.md): typed questions over host facts, additive by default; retrain heads through `scripts/bialy/`, never edit them |
| API behavior | [Security](docs/security.md), [wire policy](lycaon/AGENTS.md#openapi-and-wire-sync): `/v1`, snake_case JSON, UUID IDs, RFC 3339 timestamps, bearer auth |

## Common commands

See [dev tasks](docs/dev-tasks.md) for setup and the full reference. Run from repo root.

| Command | Use |
|---|---|
| `./task test:digest -- ./internal/foo/...` | Scoped Go tests |
| `./task den:typecheck` / `./task den:test` | Frontend checks |
| `./task budgets` | Changed code and prompt size budgets |
| `./task test:status` | Queue capacity and health |
| `./task den:harness` | Isolated UI checks; read the [harness guide](docs/dev-tasks.md#den-harness-llm-drivable-stack) first |
| `./task --list` | Available targets |
