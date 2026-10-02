# Host contract

The host contract defines how Den observes and changes host-managed state without becoming a second workflow, security, or recovery engine.

**See also:** [Architecture](architecture.md#system-shape) · [Den](den.md) · [Session](session.md) · [Compatibility](compatibility.md) · [Security](security.md)

**Machine truth:** modular OpenAPI under `docs/openapi/` (routes, DTOs, `vocab/EventTopic.yaml`, `vocab/ApiErrorCode.yaml`) · event payload schemas under `docs/schemas/events/` · generated Go (`pkg/api`) and Den (`lycaon-den/src/api/types.ts`) wire types

---

## Command discovery

Desktop startup resolves the account's login-shell PATH once. Headless launchers may supply `LYCAON_COMMAND_PATH` to select an explicit PATH without starting an interactive shell; empty, relative, malformed, or excessive entries fail startup. Discovery and command execution share the selected snapshot, including the pack board's toolchain probe: it looks binaries up in that snapshot rather than the engine's inherited PATH, runs them in parallel under a total time budget, and keeps only probes that ran to completion, keyed by the snapshot and the repository's languages. Toolchains are reported only when the execution target is this machine.

## 1. Thin-client rule

Den handles interaction and presentation. The sidecar is authoritative for consequential state and sends explicit fields for the client to render. The client must not infer any of these from transcript text:

- workflow phase, available transitions, or pending human control;
- tool outcome, reject reason, running process, or worker binding;
- approval subject, options, or installed authority;
- evidence verdict, grounding status, or closeout eligibility;
- project identity, root scope, or source revision.

The host may write human-readable markers into the transcript, but a client that needs to recognize one reads a generated shared vocabulary. It never introduces a second spelling or a general prose classifier.

Exact DTOs and routes belong to OpenAPI. Markdown explains how to use them:

| Shape | Client rule |
|-------|-------------|
| Resource DTO | Render the host's current state; do not merge it with an older local policy decision |
| UX projection | Treat it as presentation-ready host truth for the named surface |
| Event | Invalidate or patch the declared resource, then reconcile when required |
| Error code | Branch on the code and structured data, never message wording |
| Opaque id/revision | Return it unchanged with the human action it protects |

Den may keep bounded render caches. They are disposable projections and never the only copy of a host fact.

---

## 2. Prompt loop sequence

A prompt has three observable stages:

1. **Admission**: the host persists the submission and returns its stable identity.
2. **Live projection**: Den follows the active message stream for coalesced replacement snapshots.
3. **Settlement**: the durable message and session state settle; ordinary resource events invite reconciliation.

The submission id is also the durable human transcript-row id. Retries reuse operation identity, so the client can correlate acceptance, streaming, transcript, and recovery without guessing by timestamp or content.

`Session.status = busy` spans the complete visible user turn, including automatic host prompts, worker and process waits, workflow obligations, and resumable human checkpoints. Individual model and tool activities may start and stop many times inside that interval; Den may use them for live labels, but turn-final surfaces stay hidden until the session becomes idle. The idle session event and its `idle_disposition` are emitted once at that boundary, not between host continuations.

Admission precedes the turn. The host commits `busy` when execution of the admitted prompt begins, so `status` alone leaves gaps: between admission and the turn, while a command that ends without a turn runs, and between one turn's idle boundary and the next queued turn's `busy`. `Session.prompt_pending` closes them: it is true while the host holds an admitted human prompt it will run without further human action and whose turn has not begun, including next-turn draft items the host will drain. A draft held for editing waits on its person, so its items do not pend. Every session event states the value and session reads carry it. A client that shows activity while `status` is `busy` or `prompt_pending` is true never shows a session at rest while submitted work waits to start.

The sender needs one more fact, because events published before admission can arrive after admission returns. `PromptAcceptedResponse.session_revision` is minted after the receipt is durable, and every session event with a greater revision reflects the submission. The sender holds the prompt as in flight from Send until it has applied such an event, or until the request fails or replays a terminal receipt; it never releases the hold on status, a disposition, a host error, or a session read.

Live message snapshots and durable resource events are separate planes. A token is not a committed message event. Den may show an in-flight tool call or draft, but search, evidence references, and durable topic patches wait for settlement.

The host enforces a per-turn loop ceiling. The last iteration removes tools that could begin more work and retains only the appropriate completion path. A ceiling produces an honest partial or blocked outcome; the client does not relabel it as success.

### Session modes and workflow tools

Session creation attaches ambient `implement@`. A catalog start replaces the active lineage only through an explicit human action: direct start, catalog slash, armed recipe plus composer send, or a reviewed proposal card. Slash text is start intent and does not answer a phase's later feedback request. Workflow choice actions return the displayed run revision and transition id. Coordinator proposals record intent but cannot start themselves.

Tool availability is a host projection of the active leaf run, phase, surface, profile, and current resources. Den neither adds a missing workflow tool nor hides one by workflow-name convention.

### Human checkpoints

Checkpoint DTOs carry a discriminated kind and a host-authored plan. Den displays the reviewed subject and submits one opaque option id or rejection. The client does not calculate authority deltas, rewrite an action, or reinterpret a plan after a newer event. Resolution goes to the checkpoint resolver and wakes the executor only after authority and durable decision state commit.

Workflow feedback and workflow choices use their workflow resolvers. Session rewind uses the session recovery operation. Similar visual treatment does not merge these lifecycles.

### Composer elevated access

`GET /v1/sessions/{id}/elevated-access` resolves the stored root chat and returns
its effective `approvals_enabled` flag, count, redaction-safe records, and shared
scopes. Den uses these typed facts; it does not classify grant names, prose, or
commands. Disabled approval policy returns an empty presentation without
deleting saved authority. Missing lineage or policy reads must not be treated
as an empty successful summary.

`POST /v1/sessions/{id}/elevated-access/revoke` selects authority on the host at
the mutation boundary. It takes no client-selected IDs and has no bulk-ID
truncation. The existing revocation owner removes every projection and durable
record, records the person's action, publishes approval invalidation, and
returns `revoked`, `already_absent`, or `failed` per record with a fresh summary.
Install-and-seal is serialized with revocation so restart cannot resurrect a
selected record. Independent stores may partially succeed.

Den refreshes on approval/settings invalidation, reconnect, chat changes, and
expiry, with session-scoped response handling. The open lock sits next to the
external-content globe and stays hidden until enabled approvals and a nonzero
count are known. It navigates to Project configuration → Approvals → Saved
approvals, where host-classified elevated records appear first. The composer
does not revoke authority or present a revocation result. Pending or unavailable
reads never imply revocation.

### Session recovery (rewind)

Rewind is an idle-session control action that restores the boundary before a selected visible human prompt. Den displays the host's preview of affected files and conflicts, then submits its plan digest with the anchor and a stable operation id; a changed plan requires renewed review. The client does not remove transcript rows optimistically or write restored bytes: filesystem and database recovery are one host journaled transition. See [Session](session.md#session-recovery-rewind).

---

## 3. EventHub protocol

The event stream carries ordered, scoped notifications with opaque replay cursors. The envelope identifies event id, version, topic, timestamp, scope, and typed payload.

Events are at-least-once. A handler must be idempotent or rehydrate authoritative state. Event order is meaningful within the stream, but a client still uses resource revisions and generations when applying state that can change concurrently.

Some topics coalesce bursts into their latest value. Coalescing never merges a session lifecycle edge: a status change, a change in `prompt_pending`, an idle carrying its `idle_disposition`, and a host error are delivered as published, so a turn's `busy` precedes the transcript rows it opens and a quick turn's idle boundary survives the next turn's `busy`. Only updates within one status coalesce.

### Subscribe

Den may subscribe device-wide before a project is focused or project-scoped afterward. Authentication is identical to other `/v1` routes. On reconnect, the client supplies the last successfully dispatched cursor; a cursor is marked applied only after local handling succeeds, otherwise reconnect replays the event.

#### Device-scoped topics

Subscribing with a project id does **not** mean every delivered event carries one. Six topics are device-scoped and publish with no project key: `attention`, `cli_open`, `preflight`, `settings`, `providers`, and `model_policy`. `ValidatePublishScope` in `lycaon/internal/events/hub.go` is the closed roster; every other topic is refused without a project scope at publish and again at durable-outbox insert.

They are device-scoped because their subject is the device. A readiness probe naming one project still has to reach a window looking at a different one; a provider or settings change is a device fact every open window has already rendered. The consequence for a client: **do not discard an event because it has no project id.** A handler that filters on `scope.project_id === myProject` silently drops readiness, attention, provider, and settings changes, and the failure looks like a stale UI. Match on topic first.

Project registry lifecycle events (`project`) also reach every stream, because every window renders the project switcher. Their scope retains the affected project as the subject while their audience is device-wide, for live delivery and replay alike. Session, message, source, and other project content events remain filtered to the subscribed project.

### Topics

The closed event-topic vocabulary is generated from `docs/openapi/vocab/EventTopic.yaml`; this page does not mirror it. Topics fall into a few behavioral classes:

| Class | Examples of affected state | Client response |
|-------|----------------------------|-----------------|
| Resource changed | Project, session, workflow, worker, checkpoint | Patch when the payload is complete; otherwise refetch the resource |
| Collection changed | Session list, findings, scans, contributions | Refresh the bounded collection or affected page |
| Live activity | Message/process/preview state | Update the current projection without treating it as terminal |
| Global readiness | Providers, settings, host resources, notices | Recompute the relevant application projection |

Class is orthogonal to scope. Resource and collection changes are project-scoped, but the device-scoped topics cut across classes (`attention` and `cli_open` are not readiness), so a handler cannot infer a project key from the class it services.

### Heartbeat and reconnect

Heartbeat detects a dead stream without changing product state. After a window has admitted a usable workspace, reachability loss keeps the shell and locally recoverable documents available rather than replacing them with the startup stop screen; explicit host, store, and catastrophic readiness failures, and an engine the shell stopped restarting ([Supervision](#supervision)), still block normal use. Reconnect uses bounded exponential backoff. While disconnected, Den periodically reconciles authoritative project and active-session state so a long gap does not freeze the UI; a successful stream open ends that fallback polling. A replay boundary too old for retention triggers full reconciliation before live delivery resumes.

### Cache invalidation pattern

```text
host commits state + outbox event
→ event reaches Den at least once
→ Den patches a complete payload or marks the resource stale
→ Den hydrates the authoritative projection
→ render cache replaces the affected slice
```

Events do not authorize client-side speculative transitions.

**A run event precedes every transcript row that names it.** The outbox drains in insertion order, and a mutation that writes both enqueues the run event before appending rows (`enqueueRunTx` then `AppendMessagesTx` in `internal/workflow/sql_store.go`). A client never holds a span whose run is absent, so span shape is settled at first paint; rendering still tolerates a missing run, but the wire does not produce one.

### Pagination

Large collections use opaque generation-bound cursors. The host freezes ordering for a page walk so later inserts do not displace or duplicate older rows. Den follows returned cursors and refreshes when a generation expires; it does not edit cursor contents or combine pages from different generations. Request and response vocabulary: [§11](#collections-and-pagination).

### Paged source views

`/v1/projects/{id}/source/views` creates retained `tree` or `comparison` views. GET reads requested state, PATCH changes typed intent, and DELETE releases the view and its presentations. A disclosure command applies ordered changes in one intent revision; toggle resolves against accepted host intent, and Expand all and Collapse all each use one command across selected roots.

A view reports `preparing`, `ready`, or `failed`. `POST …/views/{view_id}/presentations` accepts `operation_id` and `intent_revision` and acquires a complete immutable presentation whose response carries its id and captured view summary. Nested `/rows`, `/locate`, and `/search` read only that presentation; DELETE releases it. Every access rechecks the authenticated person, project, workspace, and source/session relationship; `client_id` grants no authority. View heartbeats renew a view's presentations, which expire after thirty idle minutes.

A tree presentation retains complete visible structural coverage and an exact extent; a comparison retains immutable content and its fold projection. Neither intent changes nor filesystem observations change acquired coordinates, and reads cannot trigger discovery. Replacement preparation leaves active leases readable; capacity pressure rejects new reservations instead of evicting active leases.

Rows accept an absolute offset or an anchor with a relative offset, clamped at the presentation boundary. Optional `context_before` includes preceding context and is smaller than the frame limit; `target` identifies the resolved anchor plus offset before context is subtracted. Tree frames carry bounded prefix proofs: `retain` may submit up to 32 proofs from a previous presentation, and `retained_prefix` confirms the largest unchanged prefix. Search uses opaque query-bound cursors. Frames contain at most 200 rows and 256 KiB, plus bounded ancestor context.

Tree revisions compose directory-page fingerprints through the view's disclosure rules. Closed descendants do not invalidate visible rows. Recursive disclosure applies to newly observed descendants; explicit collapsed branches remain closed. Review preparation and filter materialization read one pinned structure generation for their whole build, and identical review facts and unchanged listings preserve the presentation revision. Weighted catalog pages are copy-on-write, retired at the generation that replaced them and swept once no pin reaches them, in a compact binary encoding identified by the ephemeral cache schema and separate from user-history formats.

POST and PATCH operation ids deduplicate before the expected intent revision is checked: exact retries do not repeat accepted actions, and reusing an accepted id with another body is a conflict. Command receipts survive for the view lifetime; creation receipts survive for the host process lifetime, so repeating a create after the view expired returns typed expiry. Host restarts discard these ephemeral resources, and the client recreates its presentation from retained intent. Den stores tree disclosure configurations in a separate device app-state slice scoped to its window and physical workspace; explicit accepted commands save that configuration before publishing new intent, and summary and row reads never write it.

`POST /source/comparison-digests` measures up to 64 comparison selectors without opening views, answering in request order: net added and removed lines, and `changes_rows` / `changes_folds`, the extent of a changes presentation and the folded runs within it. Measuring runs under the host's CPU admission, four sources at a time, and retains nothing. A failed source answers its own `failure`; `in_range: false` answers a source with no change for its file. Retained, current, and text selectors are rejected, because their sides live in a view or in the request.

`PUT …/views/{view_id}/interests/{interest_id}` replaces one consumer's viewport demand. Its presentation id fixes the coordinate system; monotonic sequences discard reordered requests. The interval is bounded to 800 rows; visible pages precede three neighboring pages on each side, with direction breaking ties. Each view retains at most eight interests, expiring after thirty seconds; `DELETE` releases one. Interests do not enter the command receipt journal or change intent, revisions, or extent.

Handle lifetime has one vocabulary, so a client never guesses why a request failed:

| Answer | Meaning | Den |
|---|---|---|
| `source_view_not_found` (404) | The host does not retain this view, presentation, or interest for the caller: it was never issued, expired, was released or evicted, belongs to another view, or was issued before a restart. Release of such a handle succeeds. | Reopens with the same intent, silently |
| `source_view_revision_changed` (409) | A basis in the request body, `base_presentation_id` or an interest's `presentation_id`, is no longer presented by this healthy view | Refetches the summary and retries |
| `session_not_found` (404) | The chat that addressed the view was deleted. Deleting a chat releases every view it addressed through the chat's resource disposal; release itself needs no live chat. | Ends the view as superseded work; the chat surface owns the absence |
| `root_not_found` (404) | An address names a folder that is not attached to the project | Reports it |
| `source_effect_not_found`, `source_history_not_found` (404) | An absent subject, such as a file edit a comparison reads | Reports it; never recreates the view |

Navigation commands can name `base_presentation_id`. The first ordinary folder toggle or nonrecursive disclosure from a displayed presentation supersedes later disclosure intent with that displayed basis, so direct folder navigation cancels a pending recursive expansion; consecutive folder commands from the same basis compose on the accepted result, and recursive expand or collapse applies to current intent and ends that sequence. A rejected command cannot partially change disclosure intent.

The project-scoped `source_view` event contains only view identity, kind, revisions, and terminal/invalidation flags: no paths, query text, source text, or failure details. Notifications are coalesced; active clients refetch authorized summaries. A refreshed summary is the head; Den retains its complete presentation and acquires a successor (see [Files stage](files-stage.md#tree)). Detaching a stage cancels viewport requests while accepted disclosure intent remains retained; reattachment or an SSE continuity gap refreshes the summary and current viewport.

## 4. Sidecar lifecycle

Den discovers the sidecar through device-local launch metadata and authenticates every protected request. A development attach mode may connect to an already-running host; release mode manages the bundled sidecar lifecycle.

Managed release startup uses a private, versioned NDJSON stream from the exact child process (`internal/startupprotocol`). The host reports ordered semantic phases, independent heartbeats, and one terminal `ready` or `failed` record; Den validates the protocol version, child PID, sequence, phase vocabulary, and terminal fields. A `ready` record is published only after the loopback listener is bound and the HTTP serving loop has started; `daemon.json` remains discovery metadata for development attach and other local clients, not managed-child readiness.

There is no elapsed-time startup failure. Protocol silence is a non-terminal stalled state that exposes progress, cancellation, and local report export; waiting continues until the child reports a result, exits, violates the protocol, or the person stops the attempt. The ordinary `serve` CLI emits the private stream only when its parent negotiates the protocol version through `LYCAON_STARTUP_PROTOCOL`.

### Supervision

The shell supervises the engine it launched for as long as the app runs. Every launch is a new generation with its own port and bearer, and the shell holds the child's output open after `ready`, so the end of that output is the moment the process exits. An exit the shell did not request is reaped, recorded in `engine.log` with its status or signal, and answered with a bounded run of restarts: three within two minutes, after short delays. One more exit inside that window, or a replacement that cannot start, stops retrying; only a person's retry starts the engine again, and it begins a fresh window. Stops the shell requests — quit, restore, restart, vault reset — advance the generation first, so they are never answered as crashes.

The shell publishes each change as `engine-state` to every window, and `engine_state` reads the current value for a window that opens later:

| State | Meaning | Window behavior |
|---|---|---|
| `idle` | No engine of the shell's own: before launch, after a requested stop, or attached to a development engine | None |
| `running` | Serving generation `generation` | A window bound to an older generation, or one that saw the engine go down, rebinds through `sidecar_info`; `SidecarInfo.generation` names the launch each connection belongs to |
| `restarting` | Exited unexpectedly; a replacement is starting | Keep the workspace in place behind an app notice; surfaces do not report their failed requests |
| `stopped` | Exited and will not be restarted | Critical stop with the exit and any start failure as its diagnostic, even after the workspace was admitted |

Health has distinct meanings: transport reachable; store and structural recovery ready; background providers or catalogs still warming; a recovery stop that needs explicit operator action. The client must not turn a warming subsystem into a global offline state. Conversely, a structural recovery failure prevents normal serve and is shown as a blocking host condition.

### Host handshake

`GET /v1/host` is the first authenticated read a client makes. It answers these questions before any other route is trusted:

| Field | Meaning | Client behavior |
|-------|---------|-----------------|
| `host_id`, `host_public_key` | The install's identity. The id derives from the Ed25519 key in the config directory (`internal/hostidentity`), survives restarts and store resets, and is never carried by a backup. | Treat a changed id as a different host: rehydrate everything rather than reuse cached state. |
| `contract_version` | The Host API contract the host serves, generated from OpenAPI `info.version`. | A different major version than the client was built for is a blocking incompatible-host condition. |
| `caller` | The authenticated person and their role. | Render authorship relative to this person ("You" is `person_id == caller.id`), never relative to a window. |
| `capabilities` | Facilities offered on this connection. `shared_device` means the client runs on the host's machine, derived from the peer address the kernel reported. | Offer host paths, the file manager, native pickers, and local editors only with `shared_device`. |

Product version (`/health.version`, `product_version`) and contract version are separate facts: a patch release can change the product without changing the contract.

Restart is a host lifecycle action Den drives natively, not an API route. Den waits for the old instance to release its store and resources, then attaches to the new generation and rehydrates; it does not preserve live process assumptions across generations.

---

## 5. Error handling

Every API error body is one envelope, the OpenAPI `Error` schema:

```json
{ "code": "invalid_query", "message": "…", "details": { "param": "limit", "reason": "…" } }
```

plus the notice presentation fields the host renders from its catalog (`title`, `suggested_action`, `actions`, `retryable`, `tier`, `scope`, `resolution`; see [Den notices](den-notices.md#the-notice-catalog)). The code defines program behavior; the message explains the occurrence. Clients branch on `code`, never on `message` text or on the HTTP status alone.

**One status per code.** Every code is lowercase snake_case and declares exactly one HTTP status in [`docs/openapi/vocab/ApiErrorCode.yaml`](openapi/vocab/ApiErrorCode.yaml) (`status:` per value). The generator emits `ApiErrorCode.HTTPStatus()`, and the Go responder (`httpio.Responder.Fail`, `FailReason`, `FailDetails`) derives the status from the code, so a handler cannot pair a code with another status. A 404 code names its resource (`session_not_found`, `scan_not_found`); bare `not_found` answers only an unmatched route. A condition with a different status or meaning gets its own code. Retired codes are deleted, never deprecated.

**Validation family.** `invalid_json` is a body that is not one well-formed JSON value of the expected shape (including an unknown field); `invalid_request` is a well-formed body whose field fails validation, with `details.field` naming the field and `details.reason` carrying the host's explanation when it has one; `invalid_query` is a malformed, missing, duplicated, or out-of-range query parameter, with `details.param`. A workflow manifest that fails validation answers `422` `workflow_validation_failed` with the diagnostics in `details.errors`.

**No error for missing wiring.** A service the host always builds is a required dependency of the route family that uses it: the family's constructor refuses to start without it, so no handler answers "not configured". A subsystem that is legitimately absent at runtime, such as one still warming or already stopping, answers `503` with its own `<subsystem>_unavailable` code through `httpio.Responder.Unavailable`. Raw Go error text never reaches a client, and every `500` goes through the logging `InternalError`.

| Error class | Client behavior |
|-------------|-----------------|
| Invalid request | Keep user input, identify the invalid field, do not retry unchanged |
| Revision conflict | Rehydrate and let the human act on current state |
| Not ready/warming | Preserve the surface and retry according to the returned condition |
| Authorization/approval | Render the host plan or denial; do not fabricate a fallback action |
| Transient transport | Retry idempotent reads with bounded backoff; preserve operation id for a retried submission |
| Projection degraded | Preserve the committed operation result, mark the affected view stale, and rehydrate or await host repair |
| Structural host failure | Stop ordinary interaction and present the host recovery action |

Retries are operation-specific. A client never blindly replays a state-changing request whose idempotency contract it does not hold.

The host does not query after commit to decide whether a command succeeded: a command response comes from its writer transaction. A delayed transcript, search row, worker card, or SSE delivery is reported and repaired as projection degradation; it is never surfaced as “the worker failed” when the worker result already committed.

---

## 6. Authentication and local zero-trust

Loopback keeps the API off the LAN; it does not prove caller identity. Protected routes require the device-local bearer token, written with owner-only filesystem permissions. The token belongs to the host owner: the host binds each authenticated request to that person, authorizes the operation for them, and filters the event stream to what they may observe.

A window or tab is a client, identified by `client_id`; a person may hold several at once. Presence, editing leases, and outbox delivery are per client. Authorship is per person.

Logs and launch metadata may expose address and resource ids to same-user processes, so UUID obscurity is not authorization. The sidecar validates token, project/session relationships, revisions, and effect policy independently. Widening the bind beyond loopback changes the threat model and is not supported by reusing the local token contract.

---

## 7. Core invariants reference

- Den renders host decisions; it does not recreate them from prose.
- Durable authorship names a person; a window or tab is a client, never an author.
- Submission identity survives retry, streaming, transcript, and rewind.
- Live message projection is separate from durable resource events.
- Events are at-least-once and lead back to authoritative hydration.
- Opaque revisions, option ids, and cursors are never re-derived by clients.
- Loopback transport still requires authentication and relationship checks.

---

## 8. Evidence file layout

Host-managed bytes must sometimes remain readable to tools without entering the project repository. The wire exposes jailed relative namespaces rather than absolute config paths.

### Agent-visible host spill paths

| Namespace | Purpose |
|-----------|---------|
| `tool-output/…` | Content-addressed oversized tool or summary bodies referenced by transcript results or the active compacted prompt view; the last durable reference reclaims the file |
| `promote-spills/…` | Bounded promotion previews and conflict material |
| `prompt-attachments/…` | Materialized user attachments and extracted bodies |

These three prefixes (`internal/tooloutput`) are the complete agent-visible set. `read` and `grep` resolve them through the project host-data provider and cannot escape to arbitrary configuration files, credentials, or another project id. Durable transcript references use the host-data-relative form so moving the config root or restoring a backup does not bake in an absolute machine path.

### Agent-visible worker-branch paths

Worker result and overlay tools expose stable overlay ids and root-relative changed paths; the model does not receive the absolute worker-branch directory. The host may resolve an overlay id for preview, verification, or promotion, but ordinary source tools remain scoped to the worker's own view or the coordinator's integration tree. An absolute engine path must never become a portable project coordinate.

---

## 9. Extensibility extension points

Extensions contribute declarative units that compile into the same host contract: commands, tools, workflows, prompts, menus, keybindings, rules, themes, and admitted external definitions. Den consumes the resolved contribution frame and invokes commands through one route; it does not import arbitrary extension code or let a pack patch client stores.

The JSON Schemas that define contributed units live at the repository root in [`/schemas/`](../schemas): anchors and anchor bindings, selectors, detection rules and packs, host resources, workflow vocabulary, and the OAR set. They are compiled with `santhosh-tekuri/jsonschema`: the OAR loader validates against them at load time, and the contract suite validates the bundled catalogs against them in the build. (`docs/schemas/` is unrelated: it holds SSE event payload schemas and the theme unit.) Resolution and authority are documented in [Extend](extend.md) and [Project overlay](project-overlay.md#project-trust).

---

## 10. Responsive project work

Responsiveness is expressed as bounded projections and explicit warming, not duplicated client logic.

- Expensive source inventories publish immutable generations. Reads may serve the last complete generation while a newer one warms only when the consumer permits stated staleness; file discovery and other correctness-sensitive reads share a short incomplete-coverage TTL, then join or force one singleflight reconciliation.
- A cold or incomplete catalog reports warming or partial rather than empty.
- Client collections paginate and virtualize; they do not require whole-history hydration.
- Background refresh is canceled or suspended when its surface no longer needs live updates. Root, source, or contribution generation changes invalidate only the projections derived from them.
- Source directory listings are bounded singleflight projections. A listing enumerates one directory, so the watch on that directory is what makes an unchanged epoch authoritative for it; coverage is reported per listing, uncovered directories are explicit with a short TTL backstop, and whole-tree reasoning still asks the root-wide question.
- Source events batch by explicit workspace kind and physical workspace id. Overflow requests resync rather than creating an unbounded queue, and shared batch-level work runs once before per-path fan-out.

### Document working sets

An open tab does not imply a live collaboration participant. The host retains explicit person/client tab references independently of presence; these prevent clean document trimming and participate in root lifecycle checks, and native inventory reconciliation removes references to vanished windows. Document metadata reads accept at most 64 identities and never open or reconcile a document. Filesystem observation selects identities in SQL by project, branch, attached root, and exact path or subtree, then loads one document at a time under its document lock.

Den admits document bodies through a per-window budget. Suspension requires durable preservation and a post-preservation interest check. Recovery carries the host replica id and incarnation; connected resumes query the host first, offline hydration precedes later reconciliation, and stale-epoch pending work stays preserved and blocked. Retention references are committed atomically with their covering checkpoint. A synchronized open with no undo writes nothing. Metadata-only event reconciliation and outbox delivery do not mount editors.

Huge-file reads use the source-view protocol with a `current` selector. The host captures an immutable, disk-backed text revision, serves bounded frames and search pages, and checks the attached root and source revision on subsequent reads. Comparison forks share the snapshot; editing limits are unchanged.

### Git status and board cache

Git and board summaries are bounded current-state projections. A change event invalidates them; it does not append an unbounded history. Exact repository operations remain in the git subsystem.

### Den poll/SSE hygiene

SSE is the ordinary invalidation path. Polling is a disconnected fallback or an explicitly bounded live-resource need. Entering and leaving a resident stage starts and stops its active subscriptions while preserving render state.

### Session create latency

Session creation commits the minimum durable identity and ambient workflow needed to accept a prompt. Naming, source warming, indexing, and other gifts run afterward; a slow optional subsystem must not hold the first durable turn open.

---

## 11. API conventions

These rules shape every `/v1` route. The mechanically checkable ones are contract tests with no exemptions: [`lycaon/test/contract/wire/api_conventions_test.go`](../lycaon/test/contract/wire/api_conventions_test.go) (`TestAPIConvention*`, each violation prefixed by its operationId or schema name). Error codes and the envelope are in [§5](#5-error-handling).

### Addressing

- A resource owned by one project lives under `/v1/projects/{id}/...`. Device resources are top-level collections (`/v1/providers`, `/v1/scanners`, `/v1/mcp/providers`, `/v1/extensions/packs`, `/v1/detection-packs`, `/v1/approval-grants`). `/v1/settings/<area>` holds singleton preference documents only, tagged `settings`.
- A `project_id` or `session_id` query parameter is a lens or filter (the project layer of a device document, a cross-project collection, the chat lens on project source), never the address of a project-owned resource. A request body never carries addressing scope: no route under `/v1/projects/{id}` accepts a body `project_id`.
- Path segments are plural kebab-case nouns. The parent parameter is `{id}`; a child is `{<noun>_id}`. A state transition on one resource is `POST /<collection>/{id}/<verb>`; a collection-level `POST /<collection>/<verb>` addresses no single resource.
- On/off state is an `enabled` boolean changed with PATCH, never an `enabled` or `disabled` path segment.
- operationIds are lowerCamel `<verb><Noun>`: `list`, `get` (GET), `create` (POST), `update` (PATCH), `replace` (PUT), `delete` (DELETE), or a domain verb for an action. An HTTP method name (`post`, `put`, `patch`) is never the verb, and a CRUD verb never names a different method than the operation's.

### Methods and status codes

- GET has no side effects.
- POST that creates answers `201` with the created resource; only POST answers `201`, and a dry run or preview never does. A POST action answers `200` with its result, `202` with a status resource for durable async work, or `204`.
- PUT replaces the whole document: its body requires every property it declares.
- PATCH merges: an absent field is unchanged and `null` resets it to the inherited or default value. That is the only reset idiom. A PATCH body requires nothing except `expected_revision` (optimistic concurrency) and `operation_id` (idempotency).
- DELETE answers `204` with no body.
- Every authenticated operation declares `401`; every operation with a request body declares `415`, and `413` where the body is bounded; a rate-limited operation declares `429` and sends `Retry-After`.
- Request schemas are closed: the body schema and every object schema inside it set `additionalProperties: false` (a composite may use `unevaluatedProperties: false`). A map-typed object declares its value schema in `additionalProperties` instead.

### Collections and pagination

- A 2xx JSON body is never a bare array and never carries `items` or `count`. A list operation (verb `list`, or one that takes `cursor`, `before`, or `after`) answers an object with exactly one plural array named for the resource, such as `{ "sessions": [...] }`. `total` appears only when it is an independent fact the UI shows. Every list that grows with user history is paginated.
- Forward pages take an opaque `cursor` and a `limit` and answer `next_cursor`, absent once the list is exhausted.
- Bidirectional windows take an opaque `before` or `after` cursor and a `limit` and answer `before_cursor` and `after_cursor`, each present only while more exists in that direction. An anchor on a known item is an explicit id parameter (`around=<message_id>`), never a client-computed ordinal.
- A positional window into one document (source view rows, chat content lines) is a coordinate, not pagination: the operation sets `x-positional-window: true`, takes `offset` and `limit` in the document's units, and reports the extent. Context around a row is `context_before`.
- No other paging vocabulary exists: no `has_more`, `more`, `next_offset`, `next_path`, ordinal or revision cursors, `git_skip`, or `page`/`page_size`, in queries, bodies, or responses. Continuation fields are opaque strings.
- Every `limit` declares a minimum and maximum. An out-of-range limit is rejected with `invalid_query` naming the parameter, never clamped.
- A bounded answer that was cut sets `truncated: true`.
- Cursors are sealed by `internal/pagecursor`: bound to their list kind, request scope, engine process, and snapshot generation where the list has one. A malformed or foreign cursor is `invalid_page_cursor`; a cursor whose process or generation is gone is `cursor_generation_expired`, and the client restarts from the first page. Handlers parse page queries with `httpio.ReadPageQuery` or `httpio.ReadWindowQuery` and answer decode failures with `Responder.PageCursorError`.

### Field vocabulary

- Names are snake_case.
- Every `id` or `*_id` property and path or query parameter that is a string declares `format: uuid`, a `pattern` for name-like ids (pack, provider, catalog ids), or a closed `enum`.
- Instants are `*_at` strings with `format: date-time`; calendar dates are `*_on` strings with `format: date`. Each format uses only its suffix.
- Durations are integer `*_ms`; retention counted in whole days may be integer `*_days`. No `_sec` or `_seconds`.
- Money is integer `*_nano_usd`, the store's unit; no other `*_usd` field exists. Den formats and parses dollars (`lycaon-den/src/cost/nano-usd.ts`); float sources convert with `cost.USDToNano`.
- `canceled` is spelled with one l. One identity has one name: `worker_id`, `delegation_id`, `operation_id` for a client-chosen idempotency key, and `blueprint` for workflow blueprints.
- Enums are closed. Event discriminators are closed snake_case enums, and event scopes carry project UUIDs.
- Every component schema is reachable from an operation, an EventTopic payload, the event envelope, or an agent tool result: a schema that is the JSON a tool returns declares `x-tool-output: [<tool>, …]` naming the platform tools that return it. A tool result names the identities it carries the way its tool's arguments do (a worker is `job_id` to an agent); every other rule applies to it.
