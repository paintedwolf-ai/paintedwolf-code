# Lycaon — agent policy

**Painted Wolf Code** is a local-first agent: Den (`lycaon-den/`, Tauri + Solid.js)
and the Go sidecar (`lycaon/`). The host owns sessions, tools, workflows, and
persistence; Den presents host state.

Read the applicable nested policy before backend or frontend work:
[`lycaon/AGENTS.md`](lycaon/AGENTS.md) · [`lycaon-den/AGENTS.md`](lycaon-den/AGENTS.md).
Start unfamiliar work at [`docs/README.md`](docs/README.md) and
[`docs/architecture.md`](docs/architecture.md). Instruction discovery and
write governance: [`docs/agents-md-standard.md`](docs/agents-md-standard.md).

## Operating rules

- **Use `./task` from the repo root for repository dev and verification targets.**
  Agents and automation must use the wrapper, even when a human uses bare `task`.
  Use digest targets for test output; never truncate raw test output with `tail`
  or `grep FAIL`. Manual/live checks have the [queue-scope exception](#queue-scope).
- **Commit only when the user asks.** Stage only your authored paths with
  `git add -- <paths>`. **Never push or otherwise publish to a remote without
  an explicit request in this conversation.** This includes sibling repos,
  tags, force pushes, PR workflows, and API uploads of git refs. A local commit,
  deployment plan, or “make it live” note does not authorize a push.
- **Preserve concurrent work and process ownership.** Follow the prohibitions
  below even when cleaning up, establishing a baseline, or freeing a port.
- **Ask first** for destructive Git/shared-state operations, discarding others'
  uncommitted work, `--no-verify`, new top-level docs or `./task` targets, and
  major dependency bumps. Prefer updating an existing hub in
  [`docs/README.md`](docs/README.md) over adding a top-level document.
- **Keep user-facing copy in sentence case**, including labels, errors, and
  prompt-visible strings. Preserve proper nouns; no title case or CSS uppercase.
  Use “Painted Wolf Code,” never “lycaon,” in product copy. Follow
  [`docs/naming.md`](docs/naming.md).
- **Report the crux, changes, verification, and unresolved work plainly.**
  If a finding contradicts the request, say so before designing around it.
  Do not agree in the reply while implementing a different decision.
  No progress updates for queued or running verification; report the result
  once it lands.

## Don't touch concurrent WIP

Dirty files you did not edit this session belong to another agent or the user.
Leave them in place. Do not revert, restore, stash, reset, or otherwise discard
those changes to tidy the tree, unstage foreign work, or “give files back.”
Omit unrelated paths from staging; do not reset them to HEAD.

Use `git diff -- <path>` to inspect your changes and scoped digests to attribute
failures. A failure in untouched work is not your blocker. To test HEAD, create
a scratch worktree and run its checks through `./task`; leave the shared tree
untouched. Undo only your own edits, preserving any pre-existing changes in the
same file.

### A repo-wide `git stash` is forbidden

**Never run `git stash` without pathspecs, under any flag combination.**
`--keep-index` still removes others' unstaged work. Path-scoped stashing is not
permission to touch foreign WIP. Use a scratch worktree for baseline checks.
If you accidentally stash the whole tree, immediately `git stash pop`, verify
working-tree and stash state, and tell the user plainly.

### Never broadcast-kill an engine

**Stop only the PID you started.** Never use command-name or command-line
matches to kill processes: no `pkill -f`, `killall`, or `lsof … | xargs kill`.
The sole exception for another owner's process is
[stuck verification](#stuck-verification): `./task test:cancel` stops a run's
processes, including orphans still holding its lease, and you run it without
asking. It never covers Den or dev servers.

For your own live verification, build with `./task build:lycaon-dev`, then run
an isolated sidecar with a scratch config directory and a probed free port.
Capture its PID and stop that PID. Read the
[isolated sidecar procedure](docs/dev-tasks.md#isolated-sidecar-verification)
before launching it.

The shared `127.0.0.1:8787` and `~/.config/paintedwolf-dev/` belong to the running
Den app. Do not use `den:sidecar` or its stop/fresh variants to prepare your own
verification. Stopping the shared engine requires user authorization; use
`./task den:sidecar:stop` only for that authorized operation. If you kill another
owner's engine, report which one and why.

## Testing

**Choose verification by the effect of your change, and run each gate once.**
For executable changes (code, build/test tooling, runtime configuration,
schemas, or shipped prompts), iterate with scoped digests on what you touched.
How you close out depends on where the change goes next:

- **Pushed for a pull request** (only when the user asked for one): push once
  your scoped checks pass. CI is the gate. Pull requests run the stages of
  `./task check-fast`; the merge queue runs `./task check` plus platform
  verification on the exact commit that lands on main. Do not run either gate
  locally first, and never rerun locally what CI passed for the same commit.
  Report the pull request without waiting on or polling its checks. A failure's
  annotation and job summary name the stage, package, and tests: reproduce that
  scope through `./task`, fix it, and push again.
- **Handed off without a push:** `./task check-fast`.
- **Full local verification:** `./task check`, only when the user asks for it
  or to reproduce a merge-queue failure that a scoped target cannot.

Do not combine both gates or repeat their subsets during the same closeout.
Merge only on request, by adding the pull request to the merge queue; never
bypass the queue or its required `check`.

For **prose-only documentation or policy edits**, review the diff, check local
links/anchors and whitespace, and verify that instructions remain consistent.
Do not submit build, lint, or test gates solely for prose changes. Executable
examples or contracts changed alongside prose need the relevant managed checks.
Never create tests that assert particular wording in documentation.

Use fixtures and `LYCAON_LLM_MOCK=1` in automated tests; no real LLM calls.
Separate live model benchmarks retain their explicit authorization requirements.
Fix root causes from digest output; do not `t.Skip` failures to get green.
Backend test conventions, including labeled errors, live in
[`lycaon/AGENTS.md`](lycaon/AGENTS.md#testing); frontend conventions live in
[`lycaon-den/AGENTS.md`](lycaon-den/AGENTS.md#testing).

**Size budgets and changed coverage run in both gates.** `./task budgets` holds
every file, directory, Go type, and prompt a change touches to its category
limit, or to an exception that says why it must be larger;
`coverage:changes` and `den:coverage:changes` require tests for the statements
a change adds. Heed the warning line: when a file you are growing passes it,
put new behavior in a new file or package. Before editing something large,
check its standing with `PW_BUDGETS_INSPECT="<path>" ./task budgets`. Answer
a failure by reshaping or trimming; an exception needs a reason a reviewer can
weigh. See
[Size budgets and changed coverage](docs/test-strategy.md#size-budgets-and-changed-coverage).

### Queue scope

**Use the verification queue, including for lint.** Builds, typechecks,
linters, automated suites, and other runner-managed checks must enter through
normal `./task` admission. One file, one package, a diagnostic retry, or a short
expected runtime does not exempt a check.

`./task den:app` is an explicit exception: its local app build and optional
`--open` launch bypass the verification queue, including while paused. Optional
`--debug` and `--no-devtools` flags retain this behavior. Other checks requested
in the same invocation still queue.

`./task den:harness` queues its build preparation separately, then releases
admission before starting the interactive sidecar and frontend. The running
session holds no verification reservation and continues during a queue pause;
new preparation waits for admission. Invoke it separately from other targets.
Automated `den:harness:test` and `den:harness:canary` runs still queue.

Do not run an underlying linter, compiler, test binary, package/digest script,
or the pinned `task` binary directly to bypass admission. Do not forge inherited
admission, change queue directories, disable the wrapper, or manipulate scheduler flags,
environment, tickets, leases, or worker counts to start sooner. Scratch
checkouts follow the same rule.

A busy, paused, slow, or broken queue is not permission to bypass it. Inspect
`./task test:status`, continue independent work, or report verification as
blocked. If an orphaned or stuck run is blocking the queue, cancel it yourself
([stuck verification](#stuck-verification)). Resume a user-requested pause only
when authorized.

**Manual UI exploration, live API probes, and bespoke reproduction or diagnostic
experiments may run outside the queue** when they do not invoke or repackage a
managed check. Calling a lint run “manual” or “bespoke” does not exempt it.
Managed preparation, such as building a sidecar, still uses `./task`; subsequent
live interaction can run independently. These checks complement required gates
and do not replace receipts. Process ownership, isolation, and live-model rules
still apply.

### Verification batches

Submit one request, inspect your own result, then choose the next. The scheduler
shares compatible work and controls admission; agents do not choose priority.
Submit setup, source generation, and maintenance separately from verification
checks: one target outside the catalog takes the whole invocation out of shared
batching, and a target nothing declares makes it exclusive.

- **Wait for completion, without polling.** `./task --background <target> ...`
  returns a job ID. Call `./task --wait <job-id>` once: it blocks until completion,
  then returns the verdict and log path. Read the exit status as the runner's
  answer — `0` passed, `1` a check failed, `2` nothing was established — and the
  JSON's `task_exit_code` as the underlying tool's own code. A `2` is not a
  broken queue; read the receipt before resubmitting. An optional
  `--on-complete '<JSON argv>'` after `--background` invokes a completion callback
  with the result JSON as its final argument. Use a completion-driven tool call;
  do not impose short wait timeouts or loop over status/log reads. Ordinary
  `./task <target>` also blocks until its result. Inspect your receipt afterward.
  **Do not end your turn while verification is pending:** when no other work
  remains, call `./task --wait <job-id>` and inspect the result before your final
  response; “done, waiting on tests” is not a completed task.
  Unsupported selection flags reject before admission; follow the reported supported flags.
- **Scope a declared target instead of guessing its flags.** Arguments after `--`
  are refused unless the target declares a selection grammar in
  `scripts/verification-plan.json`, so a scope a target cannot consume never turns
  into its whole suite. Declared Go targets narrow to packages inside their own
  scope — `./task test:integration -- ./internal/api/...` keeps the integration
  recipe and runs one subtree — and `test:digest` covers packages no declared
  target owns. `--help` after `--` is refused rather than admitted; read
  `./task --list` and the catalog instead.
- **Source is captured at admission.** Each receipt identifies the tested commit
  and tree, including saved WIP. Edits after capture need fresh verification;
  another agent's pass or an older receipt does not cover them.
- **Read your own receipt and stage logs** under `verification/<batch-id>/` in the
  [artifact root](docs/dev-tasks.md#build-outputs-caches-and-locks).
  Shared logs can contain other requests' failures. `passed` establishes the
  requested checks; `unverified` does not. Never infer coverage from target names.
- **Cancel your request, not the batch.** Withdraw one with
  `./task test:cancel -- <ticket> --reason "<why>"`, including your own: a
  duplicate a later submission replaced, or an admitted run whose captured source
  your edits have moved past. A queued request has captured nothing yet, so
  editing does not strand it and resubmitting only duplicates it. Shared execution
  continues for other requesters. Never delete active leases or source slots.
  Cancellation does not give a replacement request priority.
- **Keep caches warm.** Do not remove active cache directories or lock files;
  run `gocache:trim` only for explicit maintenance. Lower `PW_TEST_WORKERS` when
  needed; do not raise it to clear the queue.

Before changing verification infrastructure, read
[`docs/test-strategy.md`](docs/test-strategy.md#concurrency-and-type-floors) and
[`docs/dev-tasks.md`](docs/dev-tasks.md#digest-captures-and-locking).
Gate composition and Go recipes belong in `scripts/verification-plan.json`;
use `./task test:runner` for scheduler changes. Use `./task test:stress` for
scale-sensitive changes. Full suites include required scanners and saved fuzz
cases; active fuzzing and performance runs have separate nightly/release tiers.

### Stuck verification

**Decide yourself; never ask.** You decide whether to cancel a verification
run: yours, another agent's, or a human's. Never ask the user whether to
cancel it, and never ask before stopping the processes it leaves behind. Both
are already authorized. Waiting for an answer blocks every queued check behind
the run.

Withdraw a request with `./task test:cancel -- <ticket> --reason "<why>"`; the
ticket comes from `./task test:status`. A queued request withdraws freely, and
so does one whose batch still serves other requests. Work nothing else shares is
kept unless the run carries `supervision_lost`, `supervisor_stopped`, or
`suspected_stall`, or you pass `--force` — use force when you know the run is
unwanted, such as one you started by mistake.

**Cancel an orphaned run as soon as you see it.** `supervision_lost` means the
run's supervisor is gone and nobody will collect its result. Cancellation stops
every process that still holds the run's lease and lists them under `reclaimed`.
That includes processes reparented to launchd or init and processes in another
process group, such as a leftover login shell or `op completion`. Holding the
lease proves they are the run's own descendants, so stopping them is not
stopping another owner's process. If `released` is still false, cancel again
once. If it is still false, report the output as a queue bug and continue
independent work. Do not delete the lease.

`test:status` and the queue notices carry advisory observations to weigh:
`holding_queue` and `queue_backlog` say a request is serializing the host,
`runtime_exceeds_history` compares a run with completed runs of the same target,
and `source_superseded` says a batch's captured source no longer matches its
checkout, so its receipts will not cover your edits. These never change
admission and never establish that a run is broken. Long runtime, quiet output,
a queued reservation, or a backlog alone does not mean a run is stuck; read the
run's logs before deciding.

Cancel only the affected run. Do not delete active leases or broadcast-kill
processes. This exception does not cover Den, dev servers, or unrelated
processes, and cannot override a user pause. Report what was canceled and why
in your final report, as a statement rather than a question. Canceled work
remains unverified.

## Greenfield policy

Internal code that does not cross a durable or external contract can be reshaped
directly: rename and update callers, remove dead implementations and registrations,
and update tests and docs in the same change. No shims, alias forwarders, or
backwards-compatibility hedging in those internal packages.

Split large functions along real domain boundaries, with one job or named result;
do not extract arbitrary blocks just to silence lint limits. Comments are short
statements about non-obvious logic. Remove stale or obvious comments, references
to other products, historical narratives, and instructions or lectures.

Greenfield does not authorize deleting shipped interactions or visual systems,
wiping user stores, or bypassing the compatibility rules below.

Keep the identity support built for external hosts even where nothing local uses
it: the host identity private key (`internal/hostidentity`), person roles, and
per-operation and per-event authorization in `internal/people`.

## Durable surfaces

**Before changing persisted state, wire shapes, project overlays, or agent-public
vocabulary, classify the artifact using
[`docs/compatibility.md`](docs/compatibility.md).**

- Pre-v1 database changes redefine revision 1 in
  `lycaon/internal/db/schema.sql` and refresh its locks. No development migrations,
  `user_version` bumps, or dual reads. Refuse unknown shapes without modifying
  them. Released schemas require registered upgrades and verified recovery.
- Co-shipped wire changes move together: `docs/openapi/**` →
  `./task openapi:bundle` → `./task codegen:den-types` → affected Go API behavior
  and Den consumers. Never hand-edit generated OpenAPI, Den types, or Go DTOs.
  Breaking wire diffs require no acknowledgement; release review retains them.
- Prefer additive `.paintedwolf/` overlay keys; advance `overlay_format` for
  breaks. Never reuse retired public tool names or rejection-code meanings;
  deprecate them with a successor.
- Device configuration and credentials survive database wipes. Only explicitly
  ephemeral data may be discarded and rebuilt on mismatch; user history is not
  scratch.

## No heuristics

**Host control decisions must use structured facts.** Never infer intent,
grounding, workflow state, or permission floors by matching user/coordinator
prose. Branch on transcript structure, tool identity and structured rejection
`Code:`, ledger state, wire/catalog fields, confinement facts, and explicit human
actions. Human-readable errors remain diagnostics, not discriminators.

Permission floors derive from the same `confine.DefaultConfinement` boundary
the executor applies. Do not add program-name allow/deny lists or tool-specific
write roots; roots derive from OS conventions and explicit grants.

Declared protocol grammars, identifier parsing, retrieval ranking, and
host-written markers have bounded exceptions. **Before changing host gates,
permissions, confinement, provider adapters, retrieval parsing, or trust
presentation, read
[Grounding and host authority](docs/architecture.md#grounding-and-host-authority)
and the linked security/grounding contracts.** Parsing never promotes arbitrary
prose to authority.

<a id="detection-packs-overlay-not-floor"></a>
### Detection packs

Sigma packs may match execution-plane command and mediated-egress fields,
including model-authored values; chat/transcript text is not an event source.
Matches can add approval requirements and misses never establish safety.
Exactly two bundled packs, `key-material` and `credential-stores`, also supply
write-floor paths only an exact-path approval opens; they only tighten the floor and load failure stops
boot. Do not add a third floor pack.

**Before changing detection rules, events, or floor catalogs, read
[`docs/detection-packs.md`](docs/detection-packs.md).** Preserve declared versus
observed values. Policy belongs in catalog YAML, not ad-hoc Go matchers.

## Task-specific guidance

| Work | Read before editing |
|---|---|
| Tools and coordinator rejects/kicks | [Backend policy](lycaon/AGENTS.md#tools-envelope--host-coupled) and [feedback pipeline](docs/agent-tool-feedback.md): Go handlers, registry + pongo; no inline rejection prose |
| Shipped prompts under `lycaon/config/` | [Prompt policy](lycaon/AGENTS.md#agent-prompt-copy-lycaonconfig): generic defaults; project-specific tuning belongs in `.paintedwolf/` |
| Web research provider IDs | [Catalog generation](lycaon/AGENTS.md#openapi-and-wire-sync): YAML → `./task codegen:web-research-catalog`; no hand-edited provider lists |
| Decision engine: `decisions.yaml`, heads, `internal/decide`, `internal/coordinator/turnload` | [Decision engine](docs/decision-engine.md): typed questions over host facts, additive by default; heads are retrained through `scripts/bialy/`, never edited |
| API behavior | [Security](docs/security.md) and [wire policy](lycaon/AGENTS.md#openapi-and-wire-sync): `/v1`, snake_case JSON, UUID IDs, RFC 3339 timestamps, bearer auth |

## Common commands

Run from the repository root. Setup and pinned toolchain requirements:
[`docs/dev-tasks.md`](docs/dev-tasks.md).

| Command | Use |
|---|---|
| `./task test:digest -- ./internal/foo/...` | Scoped Go verification |
| `./task den:typecheck` / `./task den:test` | Frontend verification |
| `./task budgets` | Prompt and code size budgets for what your change touches |
| `./task check-fast` / `./task check` | Handoff without a push / full local verification ([testing](#testing)) |
| `./task test:status` | Queue state, blocking reasons, and advisories |
| `./task test:cancel -- <ticket> --reason "<why>"` | Withdraw one queued or running [request](#stuck-verification) |
| `./task den:harness` | Isolated UI verification; read the [harness guide](docs/dev-tasks.md#den-harness-llm-drivable-stack) first |
| `./task --list` | Find other targets instead of bypassing the wrapper |
