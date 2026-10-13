# Coordinator benchmark

The coordinator benchmark answers one question: does a candidate model, driving the **real** application, finish real coordination work? It is not a transcript score. Every verdict comes from environment facts (delivered files, executed tests, observed server requests, recorded approvals), so a model that narrates success without producing it fails.

This page states the contracts; the enforcing code is the authority for details. The runner, graders, and release policy live in [`scripts/coordinator-benchmark/`](../scripts/coordinator-benchmark/); the fixtures under [`lycaon/test/fixtures/eval/`](../lycaon/test/fixtures/eval/). The benchmark manifest is `scripts/coordinator-benchmark/benchmark.json` (`revision`, `acceptance_revision`, `application_contract`, tiers, repetition counts, and provider-availability policy).

The benchmark's `maturity` is **pilot**: its methodology is provisional and may change as real coordinator behavior exposes gaps. Every published result stays tied to its tested application, acceptance definition, model, and observed request controls. Pilot status does not waive sampling requirements or make results from changed definitions comparable.

**See also:** [Test strategy](test-strategy.md) · [Dev tasks](dev-tasks.md) · [Docs map](README.md)

---

## What it measures

The benchmark uses the actual application through the `internal/eval/toolusage` harness. `scripts/coordinator-benchmark/` freezes the application source, builds that snapshot, selects models in private configuration copies, runs the operation bank, and grades captured outcomes automatically. Paid runs require explicit opt-in (`--allow-live`); ordinary checks use fixtures and never contact providers.

Application selection and benchmark revision are independent. A run selects an application tag or full commit with `--application-ref`; moving a tag later cannot change the resolved source tree. `--application-worktree` selects a development snapshot and cannot produce a release score. The benchmark comes from the invoking checkout and is frozen separately; `--benchmark-ref TAG_OR_FULL_COMMIT` pins the evaluator independently and continues through that revision's frozen runner and dependencies. The manifest does not copy an application version; the selected application's `VERSION` supplies it.

The measured engine is a **development harness build of the selected application source**, not the signed distribution binary. Release builds disable the authenticated preparation controls, scripted workers, and manual completions this instrument uses, and every result discloses the evaluation profile. The application source identity excludes only the evaluation paths declared by the target's [`contract.json`](../lycaon/internal/harnessfixture/contract.json) (`benchmark_paths` and `harness_paths`); that declaration belongs to the application target, so a newer evaluator cannot widen it. Boundaries ending in `/` include the subtree; file wildcards match one path component. The default embedded harness comes from the application target; `--harness-ref` selects a compatible evaluation-only correction. Product code, provider adapters, prompts, policies, schema, and build scripts remain from the target.

The Go driver and evidence readers must implement the application contract the target declares. The application exposes its compiled contract through an authenticated harness endpoint, and preflight checks it before preparing cases. A contract change requires a matching evaluator revision; production storage never acquires compatibility migrations for an old benchmark. Incompatible combinations fail during preparation, before credentials or provider calls.

| Identity | Change rule |
|---|---|
| Application target | Select a new tag or commit when measured product behavior changes. |
| Application contract | Increment for incompatible harness APIs, evidence storage, or evaluation source boundaries; update the matching evaluator in the same change. |
| Benchmark revision | Increment when publishing a changed execution protocol, fixture bank, or sampling plan. Source digests identify every implementation repair within that protocol. |
| Acceptance revision | Increment when changing fixture expectations or acceptance semantics. |
| Grader digest | Computed from grading code; compatible corrections can regrade retained evidence without new model calls. |

Source inventories, file permissions, product inputs, built executables, runtime, catalogs, fixture bank, and grading inputs are retained and verified. Frozen builds disable ambient Go workspaces, inherited Go build flags, and automatic VCS stamping. Bundled tools are staged from the selected application's pins. Every capture compares its engine, catalog, and non-secret model configuration with the admitted plan, and grading rechecks the retained artifacts. These identities preserve what was measured; they do not promise identical future responses from a remote model service.

The roster is the SSOT for models, providers, worker assignment, and seed: [`coordinator-roster.json`](../lycaon/test/fixtures/eval/coordinator-roster.json). Candidate and worker reasoning use the application policy, with observed provider controls disclosed rather than a benchmark effort override.

## Operation bank and tiers

[`coordinator-benchmark.json`](../lycaon/test/fixtures/eval/coordinator-benchmark.json) is the operation bank; its project requirements and independent acceptance checks jointly define each task. The manifest assigns each operation a tier, family, scenario, and role. Thirteen scored gate fixtures in eight scenarios cover seven equally weighted families:

| Family | Required result |
|---|---|
| Current verification | Complete a changed request and verify the exact delivered source revision |
| Workspace conflict | Compose real returned overlays without losing the specified primary-tree changes |
| Progress reconciliation | Integrate returned work, verify the delivered result, and update the existing checklist; canceled work is descoped rather than marked done |
| Worker return | Resume the existing child for a partial return, integrate its completed work, and settle its application obligations |
| Closeout grounding | Apply the current request and produce model-authored, host-resolved citations to its source; host-assembled fallback does not pass |
| Fresh service | Continue through the real authorized service boundary and deliver its unpredictable receipt |
| Permission continuation | Continue after the exact prepared file action is approved or declined, delivering the authorized receipt or fallback |

The **orchestration tier** of nine fixtures in six families measures the loop the gate never enters: dispatch, park, integrate, decide, escalate, and close ([Orchestration tier](#orchestration-tier)). The **workflow tier** composes four tasks through the public workflow SDK. Each tier has its own score. `targeted-repair` and `worker-integration` carry the `calibration` role and do not contribute to any tier score.

Each trial uses its own project, application store, port, and conversation. Models receive ordinary task requests: the shortest natural request that states the user's desired result, with interface contracts in ordinary project documentation and no procedural hints or scoring language.

Preparation uses the actual application: a harness-only controller supplies scripted tool calls, scripted worker returns run through the real queue, branch, verifier, and outcome delivery, and a scripted operator answers approval cards through normal APIs with a fixed response budget. Setup has zero model token use; an unexpected setup result is an invalid measurement, never a candidate failure. Preflight validates the project baseline through the application's security overview.

## Grading

Every scored case requires regular project files and a normally completed episode. Implement tasks require a model-authored final handoff. Custom workflows require their declared terminal phase and committed, grounded verdict records; a terminal verdict need not produce a separate prose closeout. Required verification matches an invoked, completed, passing coordinator receipt to an exact snapshot of delivered source; a worker check satisfies this only when its exact source snapshot and full verification command match the delivered result. No prose classifier, tool-count target, or preferred implementation earns points; structured host and database facts decide every check, and alternative implementations are accepted where the fixture allows them.

The frozen Go evaluation driver exports a versioned episode evidence document from the application's repositories: message decoding, workflow verdict receipts, worker scopes, approval plans, invocation records, and source identity. Python grades this document and the retained deliverables; it does not query SQL or reconstruct application schema. The evidence document and driver digest are part of measurement identity. Incomplete database checkpoints or malformed evidence prevent measurement rather than silently dropping records.

Delivered source comparisons use the application's source scope and snapshot catalog. A complete hashed or Git-index snapshot must match every admitted file in the retained tree; stat-only and bounded captures cannot establish equality.

Each scenario passes only when every required fact passes. Infrastructure cannot supply a pass: missing or inconsistent preparation, configuration, capture, or oracle evidence is unmeasured, while a valid candidate execution that violates a task requirement is failed. Replacements are authorized by structured infrastructure facts, never by a low score.

Controls demonstrate both correct solutions and failures at the required boundaries. Every fixture must pass its known-good control, reject the deliberate broken alternatives, and preserve these contracts under other legitimate implementations. The pre-spend smoke (`./task eval:tool-usage BENCHMARK=test`, then `BENCHMARK=smoke -- --oracles`) exercises the assembled application without paid calls. Every prepared-worker fixture also reaches the production grader: intended trajectories pass, and untouched worker returns fail each declared promotion check. Worker checks bind to unique fixture labels and distinct child sessions.

Smoke and live runs allocate external output directories through the same authenticated harness control. The application checks each destination against its confinement write roots before recording ownership and creating it; a destination writable without a grant is a setup failure. The same Go resource owner validates ownership and removes the output after retention. Python invokes that owner through the frozen debug binary and does not duplicate confinement or cleanup rules.

A tier score averages repetitions within each fixture, fixtures within each scenario, scenarios within each family, and then the tier's families equally (`sampling.py`). Cost is disclosed alongside reliability and never changes the score; latency is an operator diagnostic. Delivered-code observations have a 30-second execution bound inside the isolated container (`candidate_process.py`); nontermination fails the relevant behavior check. Container startup and transport have a separate 600-second recovery deadline that never becomes a model failure.

## Sampling protocol

Release mode uses five attempts per fixture: **13 gate fixtures (65 episodes), nine orchestration fixtures (45 episodes), and four workflow fixtures (20 episodes)**, 130 scored episodes per model. Exploration mode uses two attempts per fixture, 52 episodes for the complete scored bank, and reports the same tier means and uncertainty bounds. Each tier requires complete measurements of its own bank; a missing measurement withholds only that tier. Family breakdowns expose pass counts and exact failed check IDs; unmeasured episodes and replacement attempts remain separate from model failures. Comparisons are descriptive: marginal intervals are not simultaneous evidence of a ranked winner. A changed fixture bank gets a new version and digest, never an undisclosed rotation.

| Scenario | Retained fixture states |
|---|---|
| Current verification | Changed limit; changed tie-break |
| Workspace conflict | Filter then paginate; normalize then deduplicate |
| Progress reconciliation | Complete returned change; preserve an intervening primary edit and descope canceled work |
| Partial worker return | Resume the existing partial result |
| Closeout grounding | Changed active filter; preserve input order |
| Fresh service evidence | Live receipt; denied service with local fallback |
| Permission granted | Direct receipt |
| Permission denied | Direct fallback |

Verification fixtures disclose the full-suite command in their README. Acceptance requires current evidence for the complete supplied suite; the coordinator can use the application verification tool or execute the disclosed command, and explicit module invocations are accepted when they cover the same complete suite.

`fixture-contracts.json` holds the hidden output contracts and probe vectors; only inputs and observation code enter the candidate container. Weights are fixed before running: equal families, equal scenarios within a family, equal fixtures within a scenario.

## Orchestration tier

The orchestration tier keeps every gate discipline: real application, scripted preparation with zero candidate tokens, byte and ledger oracles, a scripted operator, no prose classification. It combines obligations inside small projects: worker identity and dependency order, merge disposition and protected bytes, partial or failed returns and progress, and current verification and grounded closeout. Every worker the coordinator reaches is fixture-scripted (`setup.policy: scripted`), isolating coordinator choices from worker-model variability.

| Family | Fixture | Required result |
|---|---|---|
| Wave | Prerequisite join | Route different library decisions, integrate both returns, then branch for the consumer |
| Wave | Integrated review | Integrate two changes, then independently review and cite both |
| Conflict | Superseded return | Integrate wanted work, reject canceled work, and reconcile both progress states |
| Decision | Mixed returns | Land a successful sibling while retaining failed work unchanged and not completed |
| Escalation | Partially denied handoff | Deliver the independent public receipt; leave the denied private receipt absent |
| Capability | External export | Obtain the required write authority and produce the external artifact |
| Capability | Background service | Obtain listen/connect authority and refresh the receipt through the running service |
| Closeout | Resumed return | Resume the existing child, integrate its result, reconcile progress, verify, and cite |
| Closeout | Blocked validation | Distinguish current local success from a blocked required network check |

Worker prerequisites form a declared acyclic graph. Each edge is graded against job identity, the upstream promotion receipt, and the downstream creation time; later promotion cannot repair an early branch. Independent assignments may run in either order or together. Decision checks bind accepted answers to the specific job and its declared option; the right values on the wrong jobs fail.

All scored orchestration tasks are fully specified: additional `ask_user` calls fail the no-unnecessary-question check, and the bank does not grade question relevance or adaptive human answers. Approval rules name the host subject kind and select the host-authored option at the declared rung. A dependent leg must be dispatched after its prerequisite is integrated.

A scripted worker's negative verification verdict is a failed worker return, not an unavailable measurement. Missing or unverifiable verdicts remain harness errors. Preflight exercises a dependent dispatch before its prerequisite and requires a measurable failure; settled worker verdicts survive a restart without rerunning delivery.

The tier publishes three numbers beside its mean: the weighted pass^k at the release repetition count (the unbiased all-of-k estimator from tau-bench), outcome consistency (the weighted mean of (2p−1)²), and structural means per family. None of them changes a pass or fail.

## Workflow tier

The workflow tier runs ordinary project SDK workflows. The harness enables the project's workflow and prompt contributions through normal trust APIs and starts the selected workflow through the same start API Den uses. Phase tool sets, verdict schemas, named transitions, grounding audits, and advancement are the application's own behavior.

| Family | Mechanical obligation | Rejected alternatives |
|---|---|---|
| Compatible release reapproval | Select a jointly compatible production revision combination and reconcile its approvals | Individually passed but incompatible revisions, inconsistent shared dependencies, renewing an unaffected component |
| Scoped exception | Match an exception to the exact component, revision, environment and check, then apply atomic group release | Transferring an exception, using a revoked grant, releasing only the unblocked component |
| Selective handoff invalidation | Preserve the original handoffs, apply an amendment, renew the invalidated dependent and retain the independent region | Rewriting history, reusing a stale dependency binding, changing an unaffected region |
| Dependency graph recovery | Compute the transitive affected set, suspend in dependency order and restore prerequisites before dependents | Omitting a transitive dependent, restoring before its prerequisite, rolling back independent work |

Input catalogs contain a small, finite set of records. Recovery and amendment workflows expose a repeated action phase: the coordinator chooses which action to record and when to seal, and phase names do not supply the affected components or their order. Inputs and workflow files are immutable; the deliverables are committed decision records, not deployment effects.

Accepted verdict records are selected by run, phase and declared identity fields, then compared exactly with their required values and grounding audits. Each required record must occur once; extra or duplicate committed records fail. Ordering checks enforce only declared precedence edges, so independent recovery actions may appear in either order. Rejected submissions do not become accepted records. Whole-file handles and citations to required lines are both valid. A final answer cannot replace a missing record.

Fixture tests enumerate the approval combinations to establish a unique solution and all four legal recovery orderings. Application preflight executes intended paths, an alternative recovery ordering, and every named negative trajectory; each negative must fail exactly its declared checks, and field mutation controls cover missing, incorrect and wrong-type values. These controls validate the finite contracts; repeated live probes assess how models handle them. Difficulty and discrimination between strong models remain empirical questions for repeated runs.

## Application selection and benchmark cadence

Benchmark execution is an explicit operator action, independent of application publication. There is no requirement to benchmark every release and no paid GitHub workflow. `--mode release` selects the full sampling and reporting protocol, not a release trigger.

`--cadence selected` is the default and accepts the explicitly selected target, including patches and prereleases. `--cadence major-minor` filters to stable `X.Y.0` versions, including pre-v1. A skipped target creates no run directory, builds nothing, and reads no credentials. A read-only cadence decision:

```bash
python3 scripts/coordinator-benchmark/release_policy.py --version 0.9.0 --cadence major-minor
```

## Running a benchmark

Run from the repository root with Docker and the repository toolchain available. Choose output directories visible to the Docker daemon: the graders mount retained candidate files, and a host temporary directory may not be shared with a local Docker VM. The wrapper provisions pinned Python dependencies (`python_runtime.py`). Preparation does not read provider credentials or make model calls:

```bash
./task eval:tool-usage BENCHMARK=prepare -- \
  --application-ref v0.9.0 --out /absolute/path/prepared-0.9.0
```

Add `--benchmark-ref` to select a published benchmark revision. Preparation resolves the application, freezes the evaluator, builds and seals the artifacts, and exercises the deterministic application and grading controls. The result is reusable: live runs copy and verify it without rebuilding or repeating its covered preflight. Preserve the whole directory; it contains no provider credentials. If preparation is interrupted, resume its frozen selections:

```bash
./task eval:tool-usage BENCHMARK=prepare -- --resume \
  --out /absolute/path/prepared-0.9.0
```

If the invoking checkout has changed, run the preparation's retained `source/scripts/coordinator-benchmark/prepare.py` through its adjacent `python_runtime.py`. A changed evaluator requires a new preparation or an explicit repair; resume never adopts it silently.

```bash
./task eval:tool-usage BENCHMARK=run -- --allow-live --mode release \
  --prepared /absolute/path/prepared-0.9.0 \
  --source-config /path/to/private/config --out /absolute/path/results-0.9.0
```

The private configuration must contain the roster's providers and credentials. No benchmark command publishes to GitHub or deploys the website. A direct run can use `--application-ref` instead of `--prepared`; it performs the same preparation before accessing credentials. A prepared artifact retains its own runner, so later checkout edits cannot replace its implementation.

For local diagnostics, explicitly select the development tree:

```bash
./task eval:tool-usage BENCHMARK=run -- --allow-live --application-worktree \
  --models glm-flash --cases current-verification --repetitions 1 \
  --source-config /path/to/private/config --out /absolute/path/diagnostic
```

A narrowed run prepares only its selected operation boundaries and fixture oracles, plus the shared scanner/runtime and oracle-isolation controls; standalone preparation accepts `--cases` too. Coverage is recorded in the preparation receipt: a small preparation cannot authorize a larger matrix, and full preparations can serve smaller runs.

Exploration is the default mode. `--mode release` requires every scored fixture in all tiers at five repetitions; `--tier gate|orchestration|workflow|all` and `--cases`/`--repetitions` support smaller diagnostic matrices, and incomplete coverage or fewer than five repetitions cannot produce a release score. `--mode calibration` runs the calibration-role tasks separately. A partial bank never receives an overall score. Run release evaluations on an otherwise idle host with the same resources for every candidate, and freeze the roster, bank, repetition count, source and settings before the first paid attempt; never select the best attempt or add score-dependent top-ups.

Fix concurrency and provider limits before starting (`--concurrency`, `--cloud-concurrency`, `--provider-limit PROVIDER=N`); the effective policy and driver digests are frozen into the run and resume cannot change them. Each episode has a private store, project, port, and capture directory; prepared grants and denials do not spend the candidate's intervention allowance.

Observation ends when the admitted submission and its session tree have no runnable work or pending outcome delivery. A held worker or paused workflow can remain an unresolved obligation in a settled execution; its state goes to the grader. Pending descendant approvals are handled even when the coordinator is idle. The runner retains the application settlement response bound to the admitted prompt; offline collection requires that receipt before recovering a completed result.

### Repairing a benchmark against the same application

Use a new output directory and an explicit reason. The original execution and its measurements remain unchanged:

```bash
./task eval:tool-usage BENCHMARK=prepare -- \
  --repair-from /absolute/path/results-0.9.0 \
  --reason "Correct worker evidence collection" \
  --out /absolute/path/prepared-0.9.0-repair
```

The repair takes product source from the verified retained run, replaces only the external evaluator and fixtures from the current benchmark checkout, and preserves the original embedded harness unless `--harness-ref TAG_OR_FULL_COMMIT` is supplied. `--harness-worktree` tests an uncommitted harness correction from the current checkout against the retained application; it is disclosed as development source in the evaluation identity while product source stays pinned. The lineage records the reason, original execution and plan identities, and prior evaluation identity. The subsequent run uses `--prepared` and receives a new execution identity; attempts from different execution revisions are never pooled into one score. Retained application source supports repairs even after the original Git tag or object is unavailable.

If development has advanced to another application contract, base the repair on a compatible benchmark revision with `--benchmark-ref`. A harness-only correction may preserve the product release identity while producing a different, explicitly identified evaluation build. Changing measured product behavior requires a different application target; changing fixtures or acceptance semantics requires an acceptance revision; a runner repair changes the benchmark implementation digest, and a grader-only repair has its own grading digest. `report.py --regrade --out NEW_REPORT` applies compatible scoring corrections to sufficient retained evidence without model calls; it preserves the execution and manifest, rejects execution-code changes, and never overwrites the original report.

### Analysis and cost

The offline analysis command reads partial captures without provider calls:

```bash
./task eval:tool-usage BENCHMARK=analyze -- --run /absolute/path/coordinator-pilot
```

It writes `analysis.json` and `analysis.html` showing measured outcomes, failed check IDs, and spending by scenario. Cost estimates use the application's pricing pipeline; missing rates remain explicitly unpriced. `--rates /absolute/path/operator-rates.json` estimates calls whose prices are missing from the ledger; the report discloses which calls were repriced and keeps operator overrides separate from captured estimates.

Public reports include the captured cost ledger for each fixture across all isolated attempts, including worker and utility calls, preserving cache usage, price snapshots, pricing source and date, and unknown billing states. Missing captures or incomplete usage produce an explicit partial subtotal or an unavailable estimate. Host-counted usage is identified as approximate. No current price lookup changes a past result.

The website pairs tier scores with the best available estimated USD per scheduled episode: an unweighted spending average, labelled as estimated, with gaps disclosed. Only a run without usable pricing has no monetary estimate. Provider preflight is shared run overhead reported separately. Local hardware and energy costs are outside these estimates. Dollar amounts neither change verdicts nor stop execution.

Release mode writes `public.json`, the validated publication input; exploration and calibration write private `preview.json` reports. Complete exploration tiers receive pilot scores; calibration and incomplete tiers receive no score. The website importer accepts these reports for local previews and never pushes or deploys the site. Import only the public report, never the run directory.

### Supervised local runs

Prepare and validate the application first, then start an OS-supervised job:

```bash
./task eval:tool-usage BENCHMARK=service -- start --allow-live \
  --prepared /absolute/path/to/prepared \
  --source-config /absolute/path/to/credentials \
  --out /absolute/path/to/run -- \
  --roster /absolute/path/to/roster.json --repetitions 2 --tier all
```

The job uses launchd on macOS and a user systemd service on Linux (`Type=exec`, so a missing service executable fails startup explicitly). It restarts a failed controller after a cooldown and resumes the frozen plan. Foreground `BENCHMARK=run` remains available for CI jobs with their own supervisor. The Python runtime identity binds the base interpreter bytes, version and pinned dependencies, independent of the environment that invoked preparation or resume.

`BENCHMARK=service -- status --out …` inspects the job; `stop` persists a stop request and unloads only that job's service; `resume --allow-live` restarts a stopped job (successful jobs do not restart); `adopt --allow-live --out … --source-config …` supervises an existing run after its controller has exited, retaining the admitted interpreter, model selection, and episode receipts. On Linux, unattended operation beyond logout requires a persistent user manager (user lingering). macOS user agents resume at login. These services cannot run while the host is off.

Benchmark engines receive `LYCAON_COMMAND_PATH` from the launcher's PATH. Startup validates those entries without running account shell initialization, and the captured configuration records this path policy. Background-service preflight includes a server that stays running past an explicit process wait deadline; preparation requires exactly one durable timeout resume and a successful client receipt before any paid episodes are admitted.

The benchmark copies provider configuration and explicitly selects the shared adapter's availability policy. The manifest requires retries for HTTP 429, 500, 502, 503 and 504 and for unreachable or silent transports, even when local provider settings omit them; provider-specific statuses, capacity schedules and retry headers are preserved. A recoverable outage retries the same provider request with no new user prompt, episode, task allowance or tool replay. Backoff grows from five seconds to a fifteen-minute cap with jitter; a server deadline can require a longer wait. Provider cooldowns are shared and persisted. Empty completions, model refusals, invalid tool output and interrupted response bodies retain their normal disposition. Candidate execution capacity remains leased while its request waits, bounding the number of live application processes.

### Interruption, resume, and diagnostics

`status.json` and `attempts/` record durable admission and completion receipts; send SIGTERM to the owned runner PID to stop new admissions and drain admitted commands. Losing the controller does not kill active workers: resume reattaches through their leases and execution receipts, and an execution guardian retains the lease through cleanup and stops its command group if its supervisor disappears. Unconfirmed cleanup stops further admissions, including after a restart. `--resume` with the same output directory and source configuration finishes an interrupted matrix; configuration must still match the frozen plan, and completed model outcomes are never resampled.

A retryable provider failure can replace an episode only before any candidate response completes. Once the candidate has produced output, recovery must stay inside the application's existing execution; the provider's retryable flag does not authorize replaying the task, and escaped failures remain unmeasured. Every execution error retains its failure kind and code, and unknown failures do not authorize replay. An unresolved episode becomes a durable blocked slot; blocked slots never become passes or failures and do not satisfy publication's sampling requirements. Reports retain unmeasured episodes and withhold scores for incomplete tiers.

Finalization and independent grading have their own leased attempts. Confirmed process interruptions and typed provider availability failures wait without an attempt ceiling; repeated unexplained execution failures stop after eight attempts (`recovery.py`) and retain unavailable measurements without rerunning the model. Backoff deadlines and attempt histories survive restart. Grading retries require a fresh typed infrastructure failure; permanent grading failures stop automatic retries. Per-episode failures retain unaffected measurements and record the fixture, model, exception, and check identities. Completion uses the report identity and hash recorded by the selected grader, including when regrading writes to a separate output path. A shared report lease serializes publication and completion reads with standalone regrading; only a completed attempt can select its retained report for publication, and website handoff validates that report under the lease and retains an immutable publication snapshot. Handoff failures preserve the completed evaluation checkpoint, so repairing publication does not repeat paid execution.

Runner regressions inject interruptions around admission, artifact publication, receipt persistence, and result projections; process tests kill controllers and supervisors to verify reattachment, cleanup, and admission fencing without model calls. Preparation freezes the selected application and evaluator source inputs before building, then seals a manifest of the runtime and all built executables, including permissions and symlink targets; the final application marker is published last, and source identity is published atomically. Resume verifies sealed artifacts and skips rebuilding; preflight, provider admission, and grading require the same seal, and changes after sealing fail closed. Application launches discard inherited `LYCAON_*` overrides and explicitly set their harness mode, configuration, and runtime controls; coordinator, worker, and lite model request controls use application defaults and are part of captured identity. Scanner preflight tracks the requested full pass through the Security API and requires every declared member to complete before admission. The write-root control records a permission errno from a controlled write probe, checks that no target was created, then verifies the exporter after granting the same root. Preflight checkpoints its owned application store after shutdown before immutable grading; a busy checkpoint blocks admission.

Scenario design draws on coordinator activity in application logs: reduce a candidate challenge to explicit requirements and observable state, then use individual paid episodes to check that legitimate solutions pass and intended mistakes fail. Application faults do not establish model difficulty, and scenario acceptance does not depend on a particular model failing.

### Local models

Local candidates use a provider with kind `ollama` in their roster and private source configuration. Model digest, quantization, server version, sampler defaults, and template/parameter/system hashes identify the local runtime publicly; the runner checks that identity before and after each local-model episode, and a changed runtime leaves the attempt unmeasured.

### Task allowance

The bank permits **256 settled coordinator model responses per episode** (`task_allowance` in the operation bank), spanning follow-ups, host continuations, and correction responses. Attempting another response after exhaustion is a measured failure; restarts and host continuations cannot reset consumption. Empty completions, interrupted streams, transport faults, rate limits, and provider policy rejections remain unmeasured when application recovery cannot produce a completed execution.

## Precision and research

The bank's design follows published agent-evaluation work: repeated trials distinguish average success from consistency ([τ-bench](https://arxiv.org/html/2406.12045v1), [On Randomness in Agentic Evals](https://arxiv.org/html/2602.07150v1)); five trials is a reasonable starting protocol ([Terminal-Bench 2.0](https://arxiv.org/html/2601.11868v1)); and evaluation design trades measurement expense against precision ([Efficient Benchmarking of Language Models](https://aclanthology.org/2024.naacl-long.139/)). Scripted sub-agent simulation isolates orchestration quality ([OrchBench](https://arxiv.org/abs/2607.25656v1)), asking is scored against resolvable blockers ([HiL-Bench](https://arxiv.org/abs/2604.09408), [ClarEval](https://arxiv.org/abs/2603.00187)), and declared verification methods are checked against receipts ([Mind the GAP](https://arxiv.org/abs/2602.16943), [MAST](https://arxiv.org/abs/2503.13657), [Towards a Science of AI Agent Reliability](https://arxiv.org/abs/2602.16666), [DecisionBench](https://arxiv.org/abs/2605.19099)). The distinction between transcripts and environment outcomes follows [Anthropic's agent-evaluation guidance](https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents) and the independent execution checks of [SWE-bench](https://www.swebench.com/SWE-bench/api/harness/).

The benchmark aims to distinguish coarse capability gaps, roughly 15–20 percentage points, and leaves smaller differences unresolved. Per-fixture intervals use Wilson's method (`report.py`); the overall interval uses a conservative two-sided weighted Hoeffding bound computed from the declared bank weights and repetition count (`sampling.py`). The bound covers the weighted mean of independent outcomes on these exact fixtures, not new projects, local quantizations, or all implement work. Overlapping intervals do not establish equivalence, and marginal intervals are not a simultaneous ranking test.

## Calibration and readiness

The larger tasks in [`coordinator-calibration.json`](../lycaon/test/fixtures/eval/coordinator-calibration.json) challenge whether the operation scores predict sustained coordinator performance; their scores are separate measurements and are never relabeled as the operation bank or added to the public numerator after inspecting a model's outcome.

The public status remains **calibrating**. A readiness recommendation needs agreement with larger held-out tasks across more configurations and repeated runs: freeze a proposed decision rule before testing it on new held-out tasks, and do not choose a cutoff to make a favored model pass.
