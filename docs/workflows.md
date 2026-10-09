# Workflows

A workflow is a validated manifest that turns an open-ended session into explicit phases, obligations, human choices, and a durable outcome without creating a second execution engine.

**See also:** [Session](session.md) · [Coordination](coordination.md) · [Grounding](grounding.md) · [Extend](extend.md) · [Agent prompt template](agent-prompt-template.md)

**Machine truth:** shipped manifests under `lycaon/config/packs/painted-wolf/*/workflows/<id>/workflow.yaml` · manifest shape and load-time validation in `lycaon/internal/workflow/definition/manifest_yaml_types.go` · the gate and `complete_when` vocabulary in `schemas/workflow_vocabulary.json`, generated from the shipped registries by `lycaon/internal/vocabulary/export.go` · overlay checks in `lycaon/internal/workflowvalidate/`

---

## Why workflows are data

Planning, implementation, review, and human approval all need the same capabilities: model turns, tools, workers, evidence, and durable state. Encoding each recipe as a runtime branch would create several subtly different engines. Instead, a manifest declares how it obtains the user request, its phases and transitions, the agent and tool surface active in each phase, the typed gates that must pass before leaving, human feedback and approval points, nested subroutines and bounded parallel patterns, and prompt bindings and report behavior. The host supplies the vocabulary and evaluates the state; the manifest composes it. Adding a recipe changes catalog data; adding a genuinely new proof kind changes the host.

## Run control

Each `WorkflowRun` has a revision used for compare-and-swap control. A human action names the revision it displayed, so a transition cannot land on a phase that changed while the person was deciding.

Root replacement, child start, and lineage cancellation are atomic, and state commits before transcript chrome or other projections update. The active leaf run determines the current phase graph, gates, coordinator surface, and tool policy; the parent of an invoked child is paused rather than partially active.

Workflow posture changes last for the run. A terminal outcome restores the posture captured before the run started, in the same transaction as its terminal state. A child restores its parent's posture; a root restores the session's baseline.

A topology-bound phase is host-held while its declared stages settle. Independent ready stages dispatch concurrently; dependencies unlock only from complete predecessor legs. The delegation is keyed by workflow run, so startup recovery reattaches to the same durable legs and continues without asking the coordinator to repair host state.

A worker that reaches its loop ceiling with a structured budget-exhausted result enters `retry_pending`, and the topology resumes the same child session with a larger ceiling bounded by the configured host maximum. A non-retryable leg failure, an exhausted maximum, or another topology fault commits the run as `failed` with a structured code, phase, stage, and retryability flag. A failed child releases its paused parent; startup reconciliation closes the window if the process exits between those two transitions. Shutdown cancellation leaves the run running for startup recovery instead of misclassifying interruption as failure.

If a worker cannot emit a corrected grounded report, the host removes unobserved paths and URLs and binds a small sample from the evidence ledger. This settles the leg without treating model-authored citations as observed facts.

Coordinator-planned fanout uses host-issued leg ids, and each leg names its `subject` for the report checklist. `fanout.max_attempts` bounds attempts per planned leg (default one; the security survey allows two). Queue admission rejects duplicate active legs and attempts beyond the allowance. The worker-cycle gate accounts for every planned leg, including ones never started: idleness or a successful final peer cannot clear an earlier partial result. Exhausted attempts settle execution; their remaining scope stays open unless the workflow declares an evidence-backed coverage review that accounts for it. Host workflow controls (plans, coverage, gates, human responses and approval, phase skips, reviewer rosters, review attempts, execution mode, and parameters) cannot be rewritten through `state_update`; model-authored artifact inputs remain writable. Plans are retained by execution phase, and replaying a recorded native `task` call returns its original receipt even after a phase change.

A review may declare `reconciles_phase` to name the earlier claim set it addresses. Reports retain every recorded claim, preserve subsequent dispositions, and label omitted reconciliation entries unresolved. Citations establish provenance, not the truth of the model's judgment.

---

## Manifest model

A phase answers four questions:

| Question | Manifest concern |
|----------|------------------|
| What work happens here? | Agent, tool surface, prompt bindings, and optional topology |
| What proves it is done? | `complete_when`, and the `gates:` it evaluates |
| Who may leave? | Automatic, coordinator, or explicit human transition |
| Where does it go? | `next`, loop behavior, choice transition, child invocation, or terminal state |

`complete_when` takes one of four forms: `gates_satisfied`, which evaluates the phase's `gates:` list and requires a non-empty one; `gate_satisfied:<leaf>` for one named leaf; a registered predicate or gate leaf id; or a boolean expression over those identifiers. Every form is checked identifier by identifier and an unknown one fails load. `IsKnownCompleteWhen` in `internal/workflow/definition/vocabulary.go` decides; `schemas/workflow_vocabulary.json` lists every accepted identifier with its layer and domain.

A terminal phase uses `complete_when: orchestration_complete` and completes on entry, committing the run as complete in the same transaction. Terminal phases never receive a coordinator turn.

Every agent entry declares either an open-world `tools: all` surface or a named profile. `all` means the effective live registry subject to phase, posture, confinement, approval, and extension gates, not unrestricted execution. A named profile is a deliberately smaller closed surface.

Unknown fields and obsolete vocabulary fail load, so an author cannot believe a control exists when the host ignores it.

### Request contract

Every effective workflow declares a user-facing request contract. Activation and request collection are separate: attaching an ambient workflow at session creation does not pretend the user submitted an empty turn.

```yaml
request:
  cadence: once # optional; once is the default
  question: What should the review focus on?
  default: Review the project broadly against its threat model. # optional
```

On each request event the host uses the first available source: explicit text attached to the invocation, then `request.default`, then a durable open question using `request.question`. The question opens only when the submitted request is empty, so a slash remainder, API `request`, or inherited parent request starts work directly.

`cadence: once` resolves one request for the run. `cadence: each_turn` applies the contract to later turns; this is how an ambient implementation workflow accepts ordinary messages while giving an empty Send a defined result. A child workflow receives the parent request as inherited context rather than opening an unrelated question.

### Turn decisions

The host optimizes each coordinator request and worker assignment with Local AI.
Tool preloading and optional guide omission apply across workflows; manifests do
not configure this execution behavior. Required phase instructions, permitted
tools, and approval gates remain authoritative. If the engine cannot answer,
applicable guidance renders and tools remain requestable. See
[Decision engine](decision-engine.md#which-runs-decide).

## Phase structure

A phase has no kind enum. It declares structure through explicit keys, one per concern:

| Declaration | Structure it adds |
|-------------|-------------------|
| `gates:` with `complete_when` | The machine evidence the phase must satisfy before it can leave |
| `human_approval:` | A durable, Blueprint-bound human decision point |
| `invoke_workflow:` | A child workflow run with parent pause and resume |
| `bind_topology_stage:` / `bind_parallel_group:` | Host-held staged or parallel worker execution |
| `parallel_task:` | Per-phase worker concurrency caps for that execution |
| `fanout:` | Bounds on coordinator-planned legs, including attempts per leg |
| `review_loop:` | Who reviews, what evidence they owe, and which verdicts re-enter or leave |
| `intake:` | The request and context keys the phase collects on entry |
| `transitions:` | Named choice edges with their actor and gate condition |
| `explain:` | The note the host writes when it enters a phase it holds |

`phaseYAML` in `internal/workflow/definition/manifest_yaml_types.go` is the complete field list. The same declarations are available to bundled, project, extension, and session-composed workflows, subject to project trust, scope, and the gate-leaf split under [Custom workflows](#custom-workflows).

### Per-phase advance

| Policy | Meaning |
|--------|---------|
| Host automatic | Leave as soon as the declared gates pass |
| Coordinator | Expose `workflow_advance`; the call still fails if a gate is open |
| Human transition | Only a displayed, revision-bound human action may choose the edge |

Agents never select an undeclared next phase. A coordinator call requests evaluation; the host applies the transition.

### Choice transitions

A choice phase declares named edges with stable ids, labels, actor, and gate conditions. The host projects the currently armed choices; Den renders every projected choice and submits the selected id with the run revision. Human and coordinator choices use distinct action paths so an ordinary phase advance cannot impersonate a reviewed choice. Labels are presentation; the transition id and machine state are authority.

### `review_loop`

A review loop declares who reviews, what evidence they owe, and which verdicts re-enter or leave. A terminal verdict is recorded through `submit_verdict` and cites the review evidence. Review prose alone does not satisfy the gate.

A `coverage_review` verdict member binds review judgment to the host's current coverage revision. The host projects every planned area and scanner obligation plus typed scanner gaps with stable identities, scan references, counts, and bounded path samples. Each assessment supplies its fact ID, disposition, reason, evidence citations, and (for a gap) affected obligation IDs. Obligations are `satisfied`, `material_open`, or `essential_open`; gaps may instead be `covered` by alternative evidence or `immaterial`. Counts never decide materiality. Missing, duplicate, unknown, ungrounded, and stale assessments cannot advance a review phase. The reconciling reviewer challenges candidate exclusions alongside claims; the last declared coverage phase must record its own accepted assessment. Coverage revisions include review work through that phase; later report-production tasks do not invalidate the assessment. Live coverage facts are injected only during coverage-review phases.

Review repair is separate from adjudication. The workflows subsystem records rejected
`submit_verdict` results after screened transcript persistence, deduplicated by
assistant response identity. Rejections do not consume review rounds or emit
phase-progress wakes. Three consecutive identical structured defects, or eight
rejected responses in one repair episode, pause the run with `review_blocked`.
Code-only or prose-only diagnostics cannot establish an identical defect; they
count only toward the overall response limit.
Accepted verdicts resolve the episode. Startup replays unaccounted results after
the last accepted verdict; human resume starts a new episode and retains the
phase, completed work, and accepted evidence. The host also checks that current
coverage fact identifiers fit the offered schema before asking for a verdict; a
contradictory contract pauses immediately without spending model attempts.

The pause transaction retains a report snapshot: accepted verdicts, worker
reports, bound scans, coverage facts, and the separately labeled unaccepted
candidate. Its report says **Review incomplete**, assigns no success rating, and
remains downloadable after cancellation. It does not pass the phase evidence
gate. Repair episodes and snapshots are host-managed workflow variables; model
`state_update` cannot alter them.

`review_loop.coverage_reviewers` selects a subset of `required_agents` to assess coverage independently. It requires a `coverage_review` verdict member and a `reconciles_phase` carrying candidate coverage. These workers receive the host scope and candidate assessment in their normal assignment and return `complete_leg.coverage_review`. That assignment is a sealed subject with its own revision, narrower than the raw coverage facts, and the reviewer's assessment is judged against the same sealed subject at both completion and verdict admission; judging it against the raw facts would read every assessment as stale. The native completion tool returns missing or stale assessments to the same worker for repair; verdict admission also requires a successful, current structured assessment from each selected reviewer. Scope changes invalidate it; completing the reviewer itself does not. Claim-question freshness remains separately enforced. The coordinator adjudicates disagreements; the host does not infer agreement from prose.

Coverage facts include full-scope file distributions by directory and extension, warning counts by construct, and a bounded sample spread across the sorted paths. Counts and samples do not classify production or test code. Any workflow can use native `scan_query(view: coverage)` to filter warnings by `warning_kind`, `construct`, `rule_id`, or a path subtree and page locations with `offset` and `limit` (default 20, maximum 100). Summaries cover all matching warnings, even when locations are paged. Normal coverage review uses existing reviewers and budgets, with targeted source inspection rather than exhaustive warning enumeration.

Reports derive completion from that accepted review: complete means the planned obligations are satisfied with evidenced immaterial limitations disclosed; mostly means bounded material work remains; incomplete means essential work or coverage acceptance remains. Failed required scans, missing planned work, unaccounted scanner groups, and document defects remain independent failures. Open claims without an accepted question assessment also remain incomplete. The report separates remaining work, scanner limitations, assessed exclusions, and the evidence for coverage judgments. Set-aside findings are accounted inventory, not uncovered work. A retired version, whether sealed in an archive or marked `retired: true`, remains available to existing runs but is absent from the start catalog. A run whose pinned version the catalog no longer defines refuses to continue with `WORKFLOW_VERSION_UNAVAILABLE`. Each run projects its selected definition into its UI state, preserving its name, phase progress, and controls. Run visibility follows its attachment policy and ancestry, independently of start-catalog membership.

Security survey 2.0.0 adds `followup_attempts: 2` to its challenge phase. A non-terminal verdict registers each open claim's `question: {missing_fact, obligations}` in host-owned state. The returned `question/<claim id>` work ID binds focused read tasks; its `/review` work ID binds the required reviewers' reassessment. Completed investigations consume the allowance; failed providers and invalid submissions do not. The host prevents duplicate active work, requires fresh review after investigation, and permits an essential-open report for a recorded execution blocker. Otherwise material questions require resolution, evidenced immateriality, or exhausted investigation. An exhausted question cannot open another non-terminal follow-up; it must receive an explicit terminal outcome and coverage assessment. A question's materiality must agree with the obligations it affects. The report's `unresolved` finding disposition preserves unanswered questions without calling them sound or accepted risk.

The superseded 1.0.0 definition remains resolvable for existing runs as a sealed copy in `archive/security-survey/1.0.0/` and retains its coverage contract; no history, database schema revision, or recorded verdict is rewritten. Runs on a sealed version render that version's phase guidance and gate feedback from the archive.

`verdict_schema` names each member and its type: the decision enum under `verdict`, free text for any other type word, `claims` for typed claims, and `set_asides` for scanner groups the review accounts for without assessing them one by one. A set-aside is `{reason, scan_group_ids}` or `{reason, scanner, paths}`, the reason holding for every group it selects; `submit_verdict` refuses one that names a group outside the run's inventory or selects nothing once the bound scans settle. Every declared member is required, so a phase that declares set-asides states `[]` when it sets none aside. Set-asides accumulate across phases and the run report inherits them. A phase with `require_inventory_accounted: true` cannot submit a terminal verdict until the bound scans settle and every group is linked by a claim or selected by a set-aside. This check includes earlier verdicts and runs while investigation tools remain available; a rejected accounting attempt does not consume a review round. The refusal carries every unaccounted group id, full rows for the first groups, and the ids a resubmission dropped relative to the previous attempt in the same phase, so the coordinator corrects the accounting without re-paging the inventory. `submit_verdict` publishes each review phase's `verdict_schema` as its argument schema, composed by [`workflow/verdictcall`](../lycaon/internal/workflow/verdictcall) from one fragment per type word in the session's effective `submit_verdict` schema ([`submit_verdict.yaml`](../lycaon/config/packs/painted-wolf/platform/tools/schemas/submit_verdict.yaml)); the prompt and refusals show an outline of that schema, and admission validates against the schema offered that turn. A claims member whose phase declares one status word takes it by default.

---

## Authoring and validation

Validation proves that a manifest is structurally loadable, references available content, exposes the tools needed to leave each phase, and closes its declared catalog dependencies. It cannot prove that an external provider will succeed or that a human will approve an action.

```bash
./task validate:workflows
```

Project workflows use one feature directory, and the same shape moves into an extension pack without translation:

```text
{project}/.paintedwolf/workflows/<workflow_id>/workflow.yaml
```

### Custom workflows

A project workflow is user-repo content. It may compose the public phase, gate, agent, prompt, and tool-surface vocabulary, but it cannot register a new host fact or bypass effect approval. Unknown gates fail closed rather than degrading to an always-open phase.

The gate vocabulary is split, because not every leaf a bundled workflow evaluates is a fact a project can meaningfully assert. The **generic gate kit** (`gates_satisfied`, `orchestration_complete`, `child_run_complete`, `child_run_failed`, `choice_transition_required`, `human_approval`, plus the parameterized prefixes `var_equals:`, `var_truthy:`, `user_decision:`, `user_decision_received:`, `user_feedback_received:`, `hitl_consulted:`, `evidence_passed:`, `phase_is:`, `phase_skipped:`, `gate_passed:`, and the obligation prefix) is composable in a project overlay. **Domain leaves** such as `plan_stub_valid`, `topology_stage_complete`, `worker_cycle_ready`, or `delegation_closeout_complete` name state that bundled machinery owns and are refused in an overlay with diagnostic `domain_leaf_on_overlay`. Extension-pack workflows are trusted content and may use domain leaves.

`PublicGateKit` in `internal/workflow/gate_kit.go` is the enforcing source. The rule to author against: if the leaf describes something the project itself declares (a variable, a human answer, a phase position, an evidence receipt) the overlay may use it; if it describes host orchestration internals, it belongs to a bundled or extension workflow.

Session composition is bounded to one-off sequencing. Reusable behavior belongs in a project or extension workflow where it can be validated and reviewed as content.

Diagnostics carry stable codes and structured fields. Copy may change; callers branch on the code.

---

## Reference recipes

The shipped catalog demonstrates the vocabulary rather than defining special runtime paths. Runtime code does not branch on workflow ids to invent behavior outside the manifest.

### `implement@` — ambient work

The default session attaches `implement@`. Its manifest uses `request.cadence: each_turn`, so ordinary messages become the current request and an empty Send opens its declared question. It establishes orientation, loops through investigate or worker cycles, requires current source proof after mutations, and closes with a grounded completion reply.

When a root workflow finishes naturally, its completed run remains history. The next human request attaches a fresh default ambient workflow before request resolution; the same path repairs a missing active run after restart. Attachment failures block execution. Worker sessions retain their parent execution scope.

### `plan@` — research → expand → approve → execute

`plan@` has no default request. `/plan <request>` begins research directly; bare `/plan` opens “What would you like to plan?” before phase work begins. It produces a governing Blueprint, collects the research appropriate to its declared depth, expands implementation consequences, asks for human approval, and invokes `implement@` as a child during execution. The Blueprint is the durable reviewed artifact; the workflow is the process that creates and governs it.

### `options@` — decide

`/options <decision>` supplies the request and begins parallel research directly; bare `/options` opens the workflow's declared question. Parallel research and a skeptic produce evidence; the decision phase writes a durable selection report into the run's Blueprint before returning a verdict or exposing approval. Options ends after the reviewed decision is approved and never mutates source.

Bug-bash, reconnaissance, and security-survey workflows assemble the same primitives with different phase graphs, agents, evidence, and human controls.

---

## Reusable primitives

### Blueprints

A Blueprint is a governing Markdown file under `.paintedwolf/blueprints/`. It is project content: inspectable, editable, reviewable, and commit-worthy. Each blueprint carries a stable UUID `id` in its YAML frontmatter (written at creation; a hand-authored file without one has a UUID derived from its project and path until the host next writes it) and is addressed on the wire by that UUID. Its project-relative path is a field, not an address; `GET /v1/projects/{id}/blueprints?path=` filters to the blueprint at a path. All blueprint HTTP operations are project-scoped under `/v1/projects/{id}/blueprints`.

A workflow run may bind one `blueprint_path`. Approval binds the reviewed bytes, not merely the path: editing approved content supersedes the approval, and deleting it revokes the grant. A session rewind may restore Blueprint bytes because they are project content, while the authorization record follows its own lifecycle.

A phase that creates or revises the document declares `blueprint_write: true`, and validation requires its coordinator surface to expose a Blueprint-capable write tool. A Blueprint-consuming workflow must declare at least one such phase, preventing approval of an untouched scaffold.

| Concern | Authority |
|---------|-------|
| Governing content | Blueprint file |
| Current phase and gates | Workflow run |
| Approval evidence | Authorization/workflow approval record |
| Presentation | Den projection |

### Human feedback and approval

Workflow feedback is a durable run variable and transcript boundary. `ask_user` uses the same explicit-answer principle: a parked request resumes with a structured tool result, not by interpreting later prose.

`human_approval` is always bound to the run's Blueprint and the exact reviewed digest. It becomes ready only when the phase's prerequisite work is complete and the Blueprint contains material authored content. The document remains `status: draft` while awaiting review; approval, implementation, and completion are host-set lifecycle state, not claims the model may grant itself.

### Invoked workflows

`invoke_workflow` is a bounded subroutine call. Its `blueprint` field declares the document source: `inherit` passes the parent's currently approved bytes, `own` creates a Blueprint for the child, and `none` starts a documentless child. Inherited bytes are rechecked at invocation and injected into the child runtime.

Phase entry commits the parent pause and child start before any coordinator work. A post-approval wake resolves the current active leaf, so it starts the child rather than resuming the paused parent; if approval completed the root workflow, no wake is issued.

An invoked workflow may not invoke another workflow, and its child execution path must terminate. A phase may declare `child_complete_when` (and `child_gates` when it uses `gates_satisfied`) to give invoked runs a completion contract distinct from the root, and `child_next` to give the child a finite exit when the root phase loops. The host re-evaluates these contracts at successful coordinator turn boundaries and at worker and topology events. Missing targets, invalid Blueprint modes, nested invocation, and non-terminating child paths reject the effective catalog before activation.

Topology workers return findings and evidence. The workflow coordinator writes governing Blueprints and other protected workflow-control artifacts; the explicit approval phase then authorizes the mutation child.

### Obligations and injects

Phase-entry obligations are machine state. Prompt bindings render what is true and how the model can satisfy it, but they do not define the obligation. The same fact drives the active-workflow projection, blocked-transition feedback, and closeout gate.

A phase transition is a lifecycle event, not automatically new user-facing work. The coordinator wakes when the entered phase has an active obligation or newer worker, decision, or overlay state to reconcile. If the current user intent already has a committed completion report and the entered phase contains only dormant event-scoped gates, the host records the transition without requesting a second report.

Bindings are scoped by workflow and phase so guidance cannot leak into unrelated sessions. Same-phase re-entry uses an explicit event rather than pretending the phase was newly entered.

A phase's entry guidance reaches the coordinator on the next model call, whether that call opens a turn or continues one: a coordinator whose own `submit_verdict` advances the phase reads the new phase's guidance before it acts again, so a phase entered and left inside one turn is never skipped. Only the current phase's guidance is delivered; guidance for a phase the run already left is dropped with it.

### Explain notes

A phase the host holds, one with `on_enter` obligations or a topology binding, has nobody to speak while it runs. `explain:` gives it a note:

```yaml
- id: ingest
  explain:
    summary: A full security scan runs first   # chicklet title, one line, at most 72 characters
    body: >-                                     # at most 480 characters
      The review starts with a full security scan, so every later step works
      from real scanner results instead of guesses.
```

The host writes the note as a `workflow_explain` row after the phase's obligations start, stamped with the run, so it can name the records the phase waits on:

- **Obligations.** A kind that implements `ObligationPassSource` names the full scan pass its phase requested or joined; the scan obligation is one, and Den reads that pass from the folder's security overview.
- **Topology stages.** A phase that binds stages names them. Their legs come from the run's `ui.topology_legs`: the topology's plan for every bound phase, in topology order, overlaid with each dispatched leg's status, so a leg reads pending before its worker starts. A leg update refreshes the run. Pipeline stages declare a `label:` for this; a fan-out subtask's text is its label.

Den draws both with one set of progress rows, the ones the Security page uses, and counts finished scanners and workers on the collapsed row and in the composer's activity line. Progress is what the record reports, never an estimate. The note is presentation: it stays out of model history, search, and the chat's unread state, and it changes no gate. Declaring `explain:` on a phase the host cannot hold fails load.

A person who asks something while the host holds the phase gets an answer on the `await_host` surface ([Coordination](coordination.md#execution-modes)).

### Gated closeout

Closeout is a phase outcome, not merely a no-tool model message. A report-exit surface may close only when its declared gates pass: workers settled, overlays resolved, progress reconciled, verification current, required review evidence present, and citations resolvable. If a gate remains open, the host keeps the repair surface available and names the missing machine proof; it does not infer completion from phrases such as “done” or “tests passed.”

A pending coordinator question clears the gated state: a run waiting on a person is parked, not stalled.

### Reports

Downloadable reports require `controls.report.enabled: true`. A terminal run with the host-recorded `topology_report_delivered` gate or a run failed as `REPORT_NOT_ACCEPTED` uses its grounded, run-scoped completion. A run paused as `review_blocked`, or subsequently canceled, instead uses its retained incomplete snapshot. The host projects this decision as `WorkflowRun.ui.report_available`; Den and the run PDF endpoint use the same authorization.

The delivery gate requires a persisted, grounded completion whose run and phase match the current report phase and that carries no document defects; an ordinary successful turn or a completion from another phase cannot satisfy it. A matching completion stored with defects fails the run instead. Recovery can finish this transition from the saved completion after process loss. Plan and implement produce ordinary completion replies, and report document fields are taught only in an enabled report phase.

Reports are projections of durable run, evidence, finding, decision, and artifact state. The document is generated from that structured input and is not the canonical store of the outcome. It may embed durable visual artifacts by reference; missing bytes are represented explicitly.

One renderer serves every enabled report workflow, and sections follow the available records. The projection carries no workflow's vocabulary: verdict members are whatever a phase's `verdict_schema` declared, severity is styled from its rank rather than its spelling, and an unranked word renders plainly. A manifest cannot reorder, add, or remove a section. What it declares is what it calls its own conclusions:

```yaml
controls:
  report:
    enabled: true
    findings_label: Options   # Findings · Defects · Review points · Gaps
```

The document is written for a reader who was not in the chat, in third person and present tense. Page 1 is a brief with no file path, identifier, or pipeline term: how serious the outcome is, how complete the check is, what the reader is asked to decide, and what was checked. Page 2 is the working summary; the findings, narrative, adjudication, scan, and evidence follow.

**The rating is the review's call, bounded by the facts.** A workflow declares the question its reader asks, the facts each rated finding must answer, and a table from answers to levels, most severe first:

```yaml
controls:
  report:
    brief:
      question: How serious is it?
      dimensions:
        - {id: reachable, label: Reachable, question: Does untrusted input reach the flawed code?, allow_unknown: true, values: [...]}
      basis: [attacker]
      levels:
        - {label: Critical, answer: Critical risk, means: Act now., tone: critical, when: [{reachable: [reachable], ...}]}
        - {label: None, means: Nothing found that needs action., tone: good}
```

Claims carry `answers` through the review phases so a challenge can overturn them. The host rates every finding that needs attention from its answers; an `unknown` answer is tried as every value, so an open question yields a range for that finding. The level page 1 states is the report's `rating`: one declared level with the reason for it, made from everything the run did rather than from the worst table row alone. The closeout gate refuses a report without one, a level the brief does not declare, or a call milder than the level the findings' answers already decide; judgment may rate worse than the table, never clear what the facts established. A workflow that declares no brief states completeness alone.

**Completeness is the host's.** Unaccounted scanner groups, open claims, unfinished planned areas, failed scans, and a declared coverage review the host could not check because the run ended before its scans settled leave the check incomplete; partial areas, helpers that stopped short, and files that changed mid-scan leave it mostly complete. Scanner limits that recur on every run describe the scanner, not the work, and do not lower completeness.

`scan_query(view="groups")` and `scan_query(view="accounting")` expose the
same inventory revision for the same complete bound scan set. Paging echoes
`inventory_revision`; stale revisions require restarting at offset zero.
Accounting separates accepted claim links and set-asides from provisional
candidate accounting. Selector previews use every group location and identify
mixed-location and locationless groups. The `page_contract` protocol permits
wire compaction to shorten a contiguous page and recompute `next_offset`; it
never removes a middle segment while retaining the old offset.

A plan phase may require `fanout.require_task_charter`. Each leg then supplies
`done_when` criteria beside its goal, and `task(workflow_work_id=...)` resolves
that retained charter and threat model. An optional brief is coordinator
supplemental context and cannot replace the assignment. Recovery tasks retain
their explicit brief. Workers report unexamined or inconclusive scope as
`coverage_gaps` with stable local ids, subjects, reasons, and optional paths;
these become coverage facts independently of whether findings were produced.
Scanner execution status, result availability, and coverage status remain
separate host facts in worker assignments.

**Scan results are the review's input.** Every scanner group of a run's bound scans is accounted for before the report closes: a claim or finding links it through `scan_group_ids`, or a set-aside names or selects it with a reason, recorded by a review phase that declares one or by the closeout. `submit_verdict` refuses a group id outside the run's inventory, and the closeout gate refuses a document that leaves groups unaccounted. A file that changed while a full scan ran is rescanned by path and the rescan joins the run's inventory; the report names it as that rescan. The scan section lists a bounded, severity-ordered sample with closed arithmetic: every stored row is represented by a listed group or counted as not represented, and the full set stays in the scan ledger.

**Findings are conclusions, not scanner rows or claims.** The closeout authors `findings`: a plain-language title, a `disposition` (`act`, `accept` for a risk kept on purpose, or `held` for a surface examined and found sound), a severity where a grade applies, what it means if nothing is done, what to do, and where it lives. Every claim a review left open or overturned is carried by a finding with the claim's id. An `ask` states the one decision the reader is asked to make and is required when a finding is `act`. Raw scanner output travels separately as `scan_rows`.

**The report fence has one shape.** The report phase's standing guidance shows the fence as a literal object: findings with their `disposition`, `answers` for the workflow's declared rating questions, and one `ask` and one `set_asides` at the top level. A contract test holds that example to the report type the host reads. Members outside that shape are refused by name ([Grounding § Reading the report](grounding.md#evidence-grounded-prose)).

**A document the closeout could not complete is not accepted.** The closeout gate refuses a document whose fence has members the host cannot read, that breaks the report schema's rules, that leaves a claim unreported, or that leaves a scanner group unaccounted, and asks for the fence back with the pinned narrative kept; that repair has its own small budget, apart from citation repair. When it is spent, the host stores the report with every field it read and every requirement it still fails recorded as `defects` on its completion record, and the run fails as `REPORT_NOT_ACCEPTED` without satisfying the delivery gate. The report stays downloadable. Page 1 opens with "Report not accepted" and what each failed check means, ahead of any rating, and the working summary lists what each check named. A value outside a document enum, such as a disposition, is not carried onto the record; its defect names it. The rest of the document follows run facts: a claim the review left open or overturned that no finding carries is rated as its review answered it, or as unknown, never as cleared, and a report stored without an accepted `rating` states the range its answers leave. Page 1 says that no decision was recorded rather than that nothing is needed, and unaccounted groups keep the check incomplete.

**Adjudication is a timeline.** Every review phase that recorded a verdict appears in manifest phase order; the terminal decision is the last one. Each review phase with claims declares `claim_statuses`, mapping every status word it may use to `held`, `failed`, or `open`; the host reads the class, never the word. A phase with a `brief_label` is listed as a check on page 1. The reserved citation channels (`cited_evidence`, `cited_urls`) travel beside a verdict and are never projected as verdict members.

The appendix groups evidence records by what the tool observed, and a listed sample states the size of the ledger it came from. Rendering is deterministic: the same records produce byte-identical output, so the goldens under `lycaon/internal/report/testdata/` are the specification for layout, pagination, and colophon detail.

---

## Composition

| Level | Use |
|-------|-----|
| Bundled or extension | Reusable product behavior, versioned with a pack |
| Project | Repository-specific process under `.paintedwolf/` |
| Session | Bounded one-off recipe for the current conversation |

Resolution produces one effective manifest before execution. Extension switches and approved capabilities determine which contributed content is admitted; workflow validation then checks the effective graph.

## Proof layers

| Proof | Answers |
|-------|---------|
| Workflow gate | May the phase leave? |
| Tool receipt | What effect ran and how did it settle? |
| Source verification | Is a passed check current for the changed source generation? |
| Grounding | Does a claim resolve to observed evidence? |
| Authorization | What authority allowed or denied an effect? |

One proof cannot stand in for another. A successful command does not approve its own authority; an approval does not prove the source works; a grounded citation does not advance an undeclared workflow gate.

## Session concurrency

A session admits one coordinator turn at a time. Workers execute as durable child sessions with bounded concurrency and private state. Human control actions use workflow revisions. Concurrency exists in worker jobs and independent background operations, not as competing writers to one coordinator transcript or workflow run.
