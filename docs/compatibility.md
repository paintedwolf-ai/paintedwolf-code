# Compatibility

Compatibility begins with identifying what persists independently of the code that reads it. Different surfaces need different evolution rules because their failure costs are different.

**See also:** [SQL persistence](sql-persistence.md) · [Project overlay](project-overlay.md) · [Host contract](host-contract.md) · [Security](security.md)

## Why surfaces are classified

“Public” and “internal” are not enough. An internal field persisted in a user repository can be stickier than a private HTTP response shipped only between two co-versioned binaries. The relevant question is:

> What independent artifact would an incompatible change strand?

## Stickiness classes

| Class | What persists | Evolution rule |
|---|---|---|
| `durable-db` | local user history and state in the main store | preserve released baselines; apply registered upgrades; refuse unknown shapes unchanged |
| `bundle` | co-versioned Den/host wire and generated DTOs | regenerate all consumers together; retain breaking diffs for release review |
| `user-repo` | committed `.paintedwolf/` overlays and locks, including project model policy | additive first; advance the artifact's format for a breaking interpretation |
| `agent-public` | tool names, structured rejection codes, rule vocabulary, theme ids | never reuse a retired meaning; deprecate with a named successor |
| `device-config` | provider settings, credentials, and device model policy | evolve explicitly without treating database deletion as a reset |
| `ephemeral` | indexes, caches, downloaded bytes, scratch | wipe and rebuild on mismatch |

Classification follows the artifact, not the package. A database row can point to ephemeral content-addressed bytes; the row and bytes then have different compatibility classes. Lifecycle deletion recovery manifests and their referenced source objects are `durable-db` history: they travel in backups and remain retained with their pending operation or lifecycle undo entry. New human Trash operations and create/copy history plans use an additive `native_trash` receipt in the durable plan; its presence selects native recovery, and its absence preserves the retained recovery contract of existing rows. The receipt identifies an OS-owned entry and retains no bytes; removal or replacement of that entry makes restoration unavailable. Trash failure/retry fields are co-shipped `bundle` wire.

## Main store (`durable-db`)

The main store persists sessions, messages, workflows, evidence, authorization history, project records, and other facts that users expect to survive an application update.

`schema.sql` is the current fresh-install target at `SchemaVersion` (`internal/db`). Released revisions cannot be redefined, and development shapes have no reader or conversion. Only stores written by a released build are upgrade sources; preview builds that preceded 1.0.0 are not released baselines. Startup refuses any other store's unknown shape into recovery without opening a writer.

The upgrade corpus records the shape a release ships. `./task upgrade:corpus:prepare` writes a versioned fixture with an archive, semantic expectations, and checksums; preparing a candidate does not register a shipped baseline, and candidate fixtures stay editable until release. After a schema change in a candidate, seed a replacement with `./task upgrade:corpus:seed -- --out <directory>`, verify it with `./task upgrade:corpus:boot -- <directory>`, and replace the candidate fixture. `./task release:preflight -- --require-corpus` verifies the candidate's exact baseline, both database payloads, and archive integrity; a matching product version alone is insufficient. The contract suite checks the same identity during ordinary full verification.

The directory-identity migration replaces the flat current-path projection with directory relationships and entry names. It preserves immutable source versions, effects, saved pins, their ordinal and Git boundaries, and lifecycle recovery. The former `source_checkpoint_entries` table was a redundant derived projection with no read consumer; migration retires it, while comparisons continue to resolve the preserved ordinal boundaries. It does not discard retained content or user-created pins.

A schema change records the shipped source identity in `internal/db/released-baselines.json`, advances `SchemaVersion`, and adds an explicit adjacent step under `internal/db/migrations`. A step's `ID`, `Checksum`, source and destination revisions, and exact shapes identify the transformation. Every step embeds its implementation source or executed SQL as `Source`; registration verifies its SHA-256 against `Checksum` before planning. Each step declares its temporary disk requirement in `ScratchBytes`, and the complete route sums those budgets without overflow. Startup recognizes both revision and shape, validates the entire route and its applied ledger, and refuses unknown or newer stores without opening a writer. Users may skip releases: all registered intermediate steps execute in one immediate transaction, including their ledger records and revision stamp; destination shape and foreign keys must pass before commit, and a failed or canceled step rolls the entire chain back. Fresh installs do not manufacture an applied-migration history.

The application holds the installation lease before inspecting or upgrading. `db.OpenWithOptions` requires its `Before` hook to durably capture a complete recovery snapshot before any live schema transformation: copies of the database, retained bodies, and backed-up device configuration, which later live-store garbage collection cannot erase. Credentials and code-loading controls keep their separate custody rules. The `After` hook records the database commit; readiness is marked only after required retained references initialize. Failure retains the recovery point, and restoring one is explicit and explains that history returns to the capture point. A database-only `.bak` is not an installation rollback point, and opening the database does not create one merely because the product version changed.

`db.UpgradeStaged` applies the same registered transformations to an extracted backup before publication; the original archive stays untouched. A cross-file conversion prepares immutable replacement objects before committing their references and retains old material until its recovery point is released. Device-file replacements use the restore transaction's staging, pre-images, exact paths, and resume journal; a SQLite transaction never claims atomicity over filesystem writes.

The SQL registry transforms database contents only. Archive manifests, local recovery manifests, worker manifests, committed overlays, and device files have their own format versions and decoder owners. A release that changes one of those formats, or the exact backup replacement scope, supplies its versioned decoder and staged transformer with a released fixture proving the conversion. Unsupported versions are refused without mutation; no reader ships for a shape that no release wrote.

Release rehearsal requires a healthy candidate whose semantic content survives ordinary APIs, a second startup, and restoration of the released archive into another configuration root. The retained-history fixture covers transcript content, preferences, a durable approval grant, artifact media, source versions, checkpoint pre-images, a completed worker's baseline and overlay, and collaborative editing state (host draft and saved base, CRDT checkpoint and pending update, undo/redo, authorship, save receipts and reservations), which both the host and the shipped editor codecs read. An incompatible released fixture entering recovery fails the gate; separate tests prove non-destructive refusal of unsupported or corrupt stores, and synthetic migrations exercise skipped revisions, snapshot failure, cancellation, and rollback without adding obsolete production readers.

A store this build cannot open at all — damaged bytes, an unreadable journal, a permission or IO failure — is refused into the same recovery surface rather than returned as a plain start failure, because restore and start fresh are the actions that resolve it.

Recovery details are in [SQL persistence](sql-persistence.md).

### Record and blob classes

Durable identity and disposable bytes should be separate when content can be reacquired:

| Example | Record | Bytes |
|---|---|---|
| Visual artifact | durable id, provenance, state, tombstone, and content-addressed bytes | retained until explicit deletion of the artifact or its project |
| Source snapshot | durable manifest while referenced | rebuildable objects protected only while referenced |
| Search | authoritative source records | rebuildable index projection |

Compaction views are ephemeral prompt projections over retained canonical messages. Their versioned envelope may be rebuilt on format mismatch without modifying the transcript. Canonical recovery coordinates, source identities, and user records retain their durable meaning; projection invalidation does not delete them.

The recorded-tool-result path migration repairs search projections from immutable tool argument snapshots. It preserves transcript bytes and evidence bodies, updates the full-text index through its existing triggers, and uses the registered upgrade and recovery-snapshot path.

Deleting bytes must produce an explainable unavailable state, not make a durable id look as though it never existed.

## Portable command records

P03 introduces internal compiled declarations and consumer ports only. It adds no database columns, wire fields, output stores or public tool names. The existing command model call remains the aggregate receipt; child identities are host operations, never invented model calls. The introducing producer must implement the following classification, migrations and generated consumers in the same PR. P24 audits these contracts rather than supplying deferred upgrades.

| Artifact | Class and retention | Introducing responsibility |
|---|---|---|
| Immutable language/operation version, identity and catalog digest | Internal compiled facts; snapshot frozen for each invocation. Public operation/rejection meanings are `agent-public` and cannot be reused. | P03 definitions; utility producers bind actual typed operations |
| Program id, static node id, dynamic execution id, parent execution id, explicit actor/session/scope and source span | Live values are ephemeral. Once recorded in invocation history they are `durable-db`; receipt retention follows the session and preserves explainable unavailable states. Same node in a loop has fresh executions; held resumption retains one execution. | P10 receipt schema/migrations and generated consumers |
| Screened program source and complete output references, output cursor/tombstone and source/delivery mappings | Identity/provenance rows are `durable-db`; screened blobs are retained while referenced by receipts, held effects, review or recovery. Unreferenced output expires after the versioned retention period and obeys its byte ceiling. Preview truncation never truncates complete downstream bytes. Missing bytes produce an unavailable state. Never persist resolved secret values. | P07 output storage and P10 receipt/output producers; retention accompanies each producer |
| Exact execution input pin (revision, captured representation, hash, ranges) | Live snapshot ephemeral until a receipt, review or held effect references it; retained referenced bytes follow that durable record. An internal read is not model-observation evidence. | P09/P11 source/effect producers |
| Delivered mapping (response/delivery identity, exact revision, representation and input/output ranges) | `durable-db` when persisted as observation evidence. Only bytes actually delivered may establish a conversational basis. External printed paths and internal captures grant none. | P10/P11 evidence producers |
| Prepared/held effect and accepted-but-unpublished document version | Existing domain journal/history is `durable-db`; protect exact prepared inputs and recovery references until authoritative settlement. Revalidate before the single approved attempt. Accepted document state and saved publication remain distinct. | Existing mutation/editor domains; P09/P11 adapters |
| Progress admission, child terminal outcome and aggregate terminal outcome | Separate attributed facts, `durable-db` when recorded. Progress remains its current owner; child recovery stays with the invoked domain; aggregate supervision remains `processes` with `effect_attempt`, not a restartable durable interpreter job. | Existing progress/process boundaries; P10/P22 producers |
| Session job/listener/resource handles and lifecycle events | Live registry entries ephemeral, PID optional. Retained receipt/resource identity is `durable-db`; UI/event shapes are `bundle`. Session ownership survives wrappers/functions; host loss interrupts rather than resumes instructions. | P22 supervision/resource producers and corresponding wire generation |
| Variables, functions, expansion state, pipe buffers/spill staging, live capabilities, one-use permits | Ephemeral only. Bound allocations obey the versioned ceilings. No serialized instruction pointer, interpreter checkpoint, whole-program retry or crash replay. Spill retained as history follows the output rules above. | Evaluator/stream/admission producers P04/P07/P08 |
| Agent-visible command names, syntax version, status/rejection meaning, output-handle vocabulary | `agent-public`. Retire with a named successor; never reuse a meaning. DTO additions are `bundle`, authored through OpenAPI and generated together. | Each introducing producer, not frontend presentation or the later audit |

Host-loss recovery reconciles existing mutation journals and releases process/network resources, settles the aggregate interrupted, and preserves completed child outcomes. A later cancellation, refusal or stream error cannot rewrite a prior publication or HTTP effect. The invocation recorder records owner facts and cannot reclassify them. The evaluator delegates remaining obligations to existing workflow/grounding authority rather than inferring completion from stdout.

P09's compiled native selection, program attribution frame and conditional
listener admission are ephemeral, with no serialized interpreter.
`command_effects` on tool profiles, coordinator surfaces and posture
catalogs is additive configuration vocabulary; absent declarations close the new
native surface. Posture overlays intersect their shipped restriction. The new
operation/effect refusal codes are agent-public and keep actual owner and source
facts. Existing egress endpoint records may carry the additive
`authority_refused` outcome after a later payload refusal; prior connected facts
remain distinct. These changes add no OpenAPI fields or generated DTOs.

P09 also adds `execution_id` to the durable checkpoint authority descriptor for
socket/direct-IP once permits. Its canonical reader keeps session and real
model-call identity distinct from the native child. Absence or an empty value
preserves structured-only authority and grants no native child. Schema revision
5 registers the explicit revision 4 to 5 checkpoint-data transformation; revision
4 is an existing known intermediate with unchanged SQL shape, not a newly
claimed released baseline. Shipped revisions 1 through 3 and their migration
checksums remain unchanged. The step makes old structured-only scope explicit,
preserves semantic grant and plan identities, and refuses unknown authority
shapes without committing any part of the transaction. Checkpoint/session
retention remains unchanged; live permit caches remain ephemeral. The wire
projection does not publish these private authority descriptors, so this field
adds no OpenAPI DTO. P10's introducing receipt producer separately owns its next
schema step and persisted inspected reviewed-input/progress records.

Native checkpoint inspection adds a `native_arguments_representation` marker
and an explicit argument-view wrapper containing representation, canonical
inspection bytes, their digest, and the distinct original reviewed-input
digest. Schema 4 cannot assert this native format; its conversion refuses that
marker. Absence preserves the existing structured inspection shape. This data
is private `durable-db` presentation metadata under checkpoint retention and
never restored owner input or an executable argument map. The plan's original
semantic action digest remains the authority identity.

Screening traverses original scalar bytes and field names before the lossless
view is encoded. Introducing publication/export consumers must use the same
admitted lossless decoder and existing raw-value capture screening boundary;
screening Base64 text as ordinary JSON cannot prove the protected bytes were
screened. Versioned restore refuses unknown representation, digest mismatch,
noncanonical keys/tags and malformed scalar values under the host resource
ledger. A screened view's digest never replaces the original reviewed digest.

## Workflow definitions

A run pins its definition by id and version (`durable-db` state). Released
workflow definitions are contracts for existing runs. An unreleased candidate
can be corrected under its current version; the security workflow remains
`2.0.0` until that candidate ships.

Released workflow content changes receive a new workflow version. The prior
version is stored byte-for-byte under its pack's `archive/<workflow>/<version>/`,
with `SHA256SUMS` sealing the manifest, phase guidance, and gate feedback. The
archive is catalog content like any other unit, so a revision pins its bytes.
Runs on a sealed version render that version's guidance and gate feedback
before project or site overlays; sealed and `retired: true` versions refuse new
starts but let existing runs resume and finish. A run whose pinned version the
catalog no longer defines refuses to continue with `WORKFLOW_VERSION_UNAVAILABLE`.
See [Workflows](workflows.md#review_loop).

The manifest format is the engine contract, governed by `extension_api`. It
grows additively: absent or zero fields retain their prior meaning. Engine
implementation bugs can be fixed directly without inventing a new workflow
version. The optional `fanout.require_task_charter` field, for example, adds
requirements only to manifests that declare it.

Surface templates, posture rules, worker personas, and rating questions remain
shared host presentation and are not sealed with phase guidance.

Review repair episodes and blocked report snapshots are durable workflow
variables committed through the existing workflow command journal. Their new
reserved key is additive; existing runs without it have no repair episode.

Review subjects and assignments are durable run-scoped records. Schema revision
2 adds them and a review-input revision through a registered migration from the
released revision-1 shape. Existing results and verdicts remain unchanged. Released verdict receipts in
`evidence_applied` or `committed` state retain their publication acknowledgement;
upgrading does not replay already-published evidence. Active
reviews without recorded assignment context require replacement work; the host
does not reconstruct historical inputs from current state. The optional
`assignment_binding` manifest member affects only definitions that declare it.

Schema revision 3 widens the authenticator sets of the `managed_secret_reveals`
and `vault_unlocks` audits to admit `linux_user_presence` and
`linux_app_password`. The registered migration rebuilds both tables from the
released revision-2 shape and keeps every recorded reveal and unlock.

Worker `coverage_gaps` is an additive completion-report field. Co-shipped Go and
Den wire types move together, and archived workflows do not acquire the new
plan-charter requirement.

## Bundle wire and generated code

Den and the sidecar ship as one product. `/v1` is their private co-versioned boundary, but every change still moves through one source chain:

```text
docs/openapi/** -> bundled OpenAPI -> generated Den/Go types and operation bindings
```

A deliberate breaking change updates Den in the same release. `./task openapi:diff` reports against the committed baseline for release review; findings are informational, only a failure to produce the report fails the command, and it does not gate `check`. After review, advance the baseline by copying the generated bundle; Git retains the previous snapshots. See [OpenAPI release review](dev-tasks.md#openapi-release-review).

Responses remain additive-friendly: Den ignores unknown properties. Request object schemas are closed so unknown fields cannot silently change intent. Do not create multi-version adapters between co-shipped components.

Health reports product, store, and schema revisions as distinct facts. Product SemVer does not stand in for the database schema version, and the Den cache revision does not stand in for either.

## User-repository overlays

`.paintedwolf/` content is committed alongside a project and can outlive one local installation. Format numbers express structural interpretation, while extension manifest/API ranges express host capability.

Prefer additive fields. If a breaking shape needs a new explicit discriminator, advance `overlay_format` in `overlay.yaml`, the desired-state format, or the lock format rather than guessing from keys. Retain an explicitly bounded older reader only where the format contract requires one.

The shared `ignores.yaml` document requires `overlay_format: 3` in `overlay.yaml` and document `version: 1`. The `findings` and `secrets` sections have independent entry validation. Retired `scan-suppressions.yaml` and `scan-ignores.yaml` files are refused without modification, regardless of the marker. There is no migration or dual reader. Host writes publish the format marker first and preserve other keys and comments; an existing ignore document without the marker is refused. Supported older overlays without ignore artifacts remain accepted.

Secret declarations are `user-repo` content governed by the project's existing `scan_config` trust surface, without separate per-entry device approvals. Editor and chat actions write the same repository declarations. Chat review handles are ephemeral, bounded host memory and never contain plaintext in checkpoint history. Private scanner value fingerprints are `durable-db` evidence in revision 1 and are never part of scan exports. Raw findings remain authoritative when classifications change.

`source-scope.yaml` is an additive `user-repo` artifact with its own document `version: 1`: every key is optional and an absent file declares nothing, so a project that predates it reads exactly as before.

Project overlays never hold device credentials or grants. `model-policy.yaml` has two distinct artifacts: the device file is `device-config`; `{project}/.paintedwolf/model-policy.yaml` is `user-repo` content under the primary root's `overlay_format` contract.

## Automated release compatibility

Both preview and stable publication compare against the newest stable release
recorded by the signed updater feed, GitHub mirror, or immutable activation
receipt, pinned to its source commit. Activation starts a permanent publication
boundary: a failed later step cannot make that version safe to redefine. Drafts, previews, and unpublished tags
cannot reset that baseline. Missing release history, mismatched source identity,
unchanged pack versions with changed bytes, workflow version or archive violations,
insufficient extension API declarations, and corpus mismatches block publication.
Published preview database revisions remain supported upgrade sources even though
they do not become the stable compatibility baseline.

The first release stages check channel progression, packaging versions, release notes,
and static corpus integrity before native setup or application builds. When publishing,
the candidate must be newer than every publication in its channel (or match the exact
tagged commit on a rerun after partial publication), and `RELEASE_BUILD` must
exceed every other published build across both channels; dry runs still reject downgrades
and reused versions at different commits. Signed pointers, all published GitHub releases, and immutable activation receipts
count; drafts and unactivated tags do not. Activation receipts retain obligations
when a GitHub mirror fails or a channel pointer later advances. SemVer 2.0 precedence applies to preview names: use dot-separated numeric prerelease identifiers (`-rc.N`) so multi-digit candidates sort numerically (`1.0.2-rc.10 > 1.0.2-rc.9`) across both release gates and the native updater.
Full database shape and upgrade rehearsal checks follow with the built candidate.

The release controller is pinned to `refs/heads/main` and reads the selected candidate tree
(including [`agent-public-retirements.json`](../schemas/agent-public-retirements.json)),
so neither a tag push nor an older qualified source can select an older compatibility gate.
OAR fact signatures and rejection-code meanings (`category`, `emit`, `severity`) cannot change under a surviving name. Removed facts and
rejection codes require named successors in
[`agent-public-retirements.json`](../schemas/agent-public-retirements.json), and
retired names cannot return; retiring a vocabulary item with a named successor does not
force a major bump of `painted-wolf/platform`, preserving `^1.0.0` compatibility for
existing extension locks. Conversely, any pack whose `policy/*.yaml` requires a host fact
absent from the baseline release must advance at least a minor version and raise both its
`extension_api` and `painted-wolf/platform` floors above the baseline. These checks validate structured contracts; they do
not infer behavioral equivalence from display copy.

### Release coverage

Product versions identify publications. Other identities advance only when their
own contract changes. An additive configuration field does not require a new
store schema, and a regenerated cache does not justify changing an archive format.

| Surface | Independent contract | Automated assurance |
|---|---|---|
| Product and packages | `VERSION`, `RELEASE_BUILD`, native projections, channel | Early checks require channel advancement, globally increasing builds, matching package metadata, and release notes. Source tags and exact-commit qualification are mandatory. |
| Main store and migration history | Revision plus canonical shape; immutable migration IDs and checksums | Every published post-v1 corpus must remain byte-for-byte identical to its tag and retain a supported schema identity. Early checks also preserve published migration IDs and checksums. Compiled migration registration validates complete routes; built-candidate preflight and upgrade/restore rehearsal validate actual stores. |
| Backups and recovery | Archive format, store identity, exact replacement scope, journal formats | Corpus integrity checks run early. Backup integrity, relocation, recovery, unknown-format refusal, and restart tests run during qualification; release rehearsal restores a released archive. |
| Worker branches and retained source | Worker metadata, seed/package manifests, promotion and rewind journals | Workspace, checkpoint, source-ledger, and promotion recovery tests preserve retained bytes and resumable operations. These formats are independent of product SemVer. |
| Project configuration | Overlay format, extension manifest/desired-state/lock formats, ignore document version | Format, transition, unknown-shape refusal, dependency-lock, and catalog publication tests run during qualification. Compatible stock updates are resolved against declared constraints. |
| Extension and suite releases | Pack version, suite version, extension API requirement | Early checks require changed pack/suite bytes to advance their own release identity and reject API regressions or unavailable capabilities. Suite membership has its own version; member content changes do not change membership. |
| Workflows and prompt content | Workflow ID/version, sealed archives, persona contract, prompt-unit identity | Early checks preserve released workflow bytes and archives. Persona, prompt rendering, bindings, and unit/catalog validation run during qualification. Copied prompt overrides need the explicit repairs above. |
| Agent and extension vocabulary | OAR facts, rejection codes, tool names/runtime prefixes, workflow terms, anchors, theme IDs | Early checks reject unnamed removals, retired-name reuse, changed fact/term signatures, and missing successors. Anchor contract changes advance the catalog's minor or major version as appropriate. |
| OAR language and authoring schemas | Vendored spec/capability version and pinned source digests | Vendor integrity, schema synchronization, conformance, and catalog tests run during qualification. A pack version does not authorize changing a language contract. |
| Den/host and native IPC | Generated OpenAPI, startup/document/decision protocols | OpenAPI evidence is retained against the pinned stable source. Drift, generated-consumer, native protocol, and platform tests run on the qualified commit. Co-shipped protocols move with both consumers. |
| Device settings and credentials | App-state/preferences formats, exact grants, identity and vault custody | Strict decoding, explicit updater format-1 conversions, unknown-journal preservation, app-state normalization, credential custody, and store-reset tests run during qualification. A database wipe is never their migration. |
| Update distribution | Feed schema, signatures, signing generations, platform/package manifests | Early reads verify signed channel pointers and retain every published public key, closed bridge, and signing/embedded generation. Registry, key-transition, manifest, platform, credential, immutable-publication, and replay checks gate later publication stages. |
| Browser/media artifacts and exported findings | Timeline/archive and findings format versions | Archive reader/writer, retained-reference, and output-format tests run during qualification. Stored artifact formats remain independently interpreted after an update. |
| Models, decisions, and ephemeral caches | Decision/catalog format, generated head identity, content revisions, cache schemas | Generation drift, native/head contracts, and invalidation tests run during qualification. Disposable data may be rebuilt on mismatch; recovery journals and retained source may not. |

The release controller performs the early structural checks for either channel
before application/native setup. Full qualification on the exact selected commit
supplies the behavioral and platform evidence; the early gate does not replace it.
Neither stage adds a human acknowledgement requirement.

## Extension identities

`extension_api` selects host interpretation and capabilities. A pack's `version`
is its semantic release identity; workflow versions separately pin resumable run
content. Catalog revisions identify captured effective content and configuration.
None substitutes for another. Content changes can move a catalog revision without
changing a pack version, while a version bump alone does not prove compatibility.

## Prompt overrides

Project and site prompt overrides persist independently of the bundled templates.
Their include paths, required persona sections, unit slots, and template variables
must be considered when changing prompt composition. A missing dependency fails
rendering; the host does not silently omit it. Validate an override against the
candidate's template graph before using it.

Overrides copied from 1.0.1 must remove includes of
`partials/coordinator-mode-investigate-progress.md` and
`partials/coordinator-progress-closure.md`. The host now composes progress
instructions through `shared/units/coordinator-progress-procedure.md` in the
`procedures` slot. Keep `{{ units.procedures }}` in the parent template instead of
including that unit directly, so its tool and surface eligibility still applies.
Overrides of `guidance/coordinator-report-document.md` must consume its structured
issue and retained-document variables instead of `drafted_synthesis`.

## Agent-public vocabulary

Structured tool names, `Code:` meanings, rule effects, theme tokens, and other model- or extension-authored ids are contracts because external text or content can name them. Virtual root prefixes such as `@scratch` are likewise agent-public path vocabulary, and no attached root may carry a virtual root's label.

- do not reuse a retired id for a different meaning;
- mark it deprecated and name a successor;
- keep the transition explicit in the defining catalog or schema;
- remove the old path only under the published retirement rule.

Free-form display copy is not agent-public vocabulary. The structured id behind it is.

## Device configuration

Provider credentials, device model policy, extension desired state, exact grants, and application state are not contained in `store.db`. Deleting the database must not silently erase or reinterpret them.

The host identity key (`host-identity.pem`) is device configuration too. A store reset keeps it, and backups neither carry nor restore it: a backup restored on another machine becomes a new host with the same people. An unreadable key stops startup rather than silently minting a new identity. The people who author durable facts live in `store.db` and travel with it.

Native update preferences are device configuration too. The unversioned `{checks_enabled, channel, rollout_bucket}` record that 1.0.x clients wrote has one explicit migration to `updates/preferences.json` format 2: `checks_enabled` becomes `automatic_updates_enabled`, which governs discovery, background download, and lifecycle installation, so an existing opt-out remains off; channel and bucket are unchanged. Unknown versions and malformed records are refused without overwriting them. Per-installation operational records live under `updates/installations/<path-sha256>/`, keyed by the canonical application path, and the device-wide `updates/feed-state.json` records the newest signed pointer timestamp accepted per feed. Unknown ephemeral records (`ready.json`, `rejected.json`, `feed-state.json`) are quarantined unchanged and rebuilt; an unreadable preference file disables automatic updating for that run without being rewritten. Update archives are ephemeral, but an unresolved activation journal is recovery-critical operational state and cannot be cleared as a cache. An `update-state.json` journal written by a 1.0.x client is reconciled against the running product version: it completes only when its target or a newer version runs, and it never manufactures staged bytes or missing artifacts.

Security-sensitive grants remain exact. A socket-path grant must not widen to its containing directory during evolution. Credentials stay outside backups and diagnostics regardless of format version.

Native update `ready.json` format 2 records the exact prepared bundle path and a nullable offer confirmation. Its strict format-1 decoder retains the legacy prepared path and confirmation. `transaction.json` format 2 also names the prepared path; its strict format-1 conversion first saves the original record under `receipts/<id>-format-1.json`, then atomically publishes the converted journal. Unknown journals remain untouched. New preparation names include the account UID, so accounts sharing an application cannot remove each other's preparation. The installed bundle and its parent directory provide cross-account inode locks; private journals never cross account boundaries. The permanent profile-wide `feed-state.lock` serializes replay-state reads, quarantine, and writes. `records.lock` serializes ready, rejection, and journal mutations. Lock files are never replaced or removed during normal operation.

Secret-release leases use `secret_recipients` and a witness bound to that exact recipient set; a destination-only lease cannot establish recipient authority. Startup refuses incompatible entries, including expired ones, and leaves the entire approvals file unchanged. Its error identifies the file and either the invalid YAML line or the invalid grant ID.

An unknown `approval_posture` likewise stops startup with the file unchanged, and the error names the file and the token; correct it to `light`, `balanced`, or `strict` and restart. It is never read as the default, which would quietly lower an ask-line someone chose.

To recover, make a private copy of the reported file, remove only the affected `category: secret` entries from its `grants` list, and restart. Preserve the other grants, rules, posture, and preferences. The next secret release requires a new approval for its exact recipients. Do not translate destination strings into recipient authority or delete the approvals file or database to recover.

## Ephemeral data

Search indexes, web indexes, source-observation indexes, model and pricing feeds, downloaded extension bodies, browser cache, debug logs, scan scratch, sealed worker branch trees, and unclaimed staging data can be cleared and rebuilt. A worker branch tree is rebuildable only once its overlay is sealed at completion; the baseline and overlay manifests that rebuild it are source history, not scratch. Retained project attachments, artifacts, tool-result spills, and source history are not scratch. The local-data API defines the closed bucket inventory; Den projects it rather than carrying a duplicate list.

Session scratch (`session_scratch`) is clearable chat working data, not a cache: nothing rebuilds it, and transcripts may name its files. Every chat and worker has a private folder under the host's scratch root. A folder lives until its chat is deleted or a person clears the bucket; clearing keeps the folder of any chat with a turn or process in flight. Scratch is excluded from backups and removed with a store reset.

Ephemeral does not mean unbounded or invisible. Each cache still needs a lifecycle controller, size/retention policy, clear operation, and an explainable unavailable state. Restore pre-images are held to the same rule: the registry names them, measures them, and reclaims the ones no transaction still needs.

## Backup & restore

A backup captures the durable local installation, including retained project host bytes, source-history objects, draft workspaces, and rewind checkpoints. It excludes what must be reacquired or configured separately: credentials, MCP definitions and OAuth state, extension desired state, daemon and live process tokens, caches, and code-loading control-plane files. Provider metadata, device model policy, approvals, limits, scanner/review settings, project trust settings, and application preferences are durable user choices and are restored.

The archive contains a manifest with format, product and schema versions, a digest of the store shape it carries, exact replacement scope, file kinds, permission bits, sizes, and hashes. Creation and upload stream through disk, so archive size does not become process memory. The host stages and validates the entire archive, including SQLite integrity, before it snapshots any live replacement path, then creates recovery copies and publishes a restart marker. Startup applies the staged snapshot before opening the store; a failed apply leaves the process unavailable, records progress for safe resumption, and retains the recovery copy. The declared replacement scope is exact: it never merges unknown leftovers within a restored directory. Nested regular files, executable bits, and symbolic links in draft workspaces round-trip without following link targets into the archive.

Export and full restore check retained file references from the captured database against the archive inventory: stored source revisions, model and evidence bodies, live artifacts, retained attachments, transcript spills, and worker baselines must be present. Metadata-only revisions and deleted artifacts do not require bodies. If concurrent deletion or a changing file prevents a complete capture, export fails without publishing an archive; retry after active changes finish. This check does not pause the engine or promise a single filesystem-wide instant.

Upgrade recovery points use the same self-contained durable scope as manifest-backed directories under `upgrade-recovery/{UUID}/`. SQLite uses its online snapshot API; regular payloads use kernel copy-on-write clones on supported filesystems. On Linux, canonical read-only source-content objects use hard links when cloning is unavailable; mutable files and cross-filesystem bodies use a checked streaming copy. Shared source objects retain their original read-only modes and remain available after live-store collection. Each retained file and the complete manifest have integrity digests. Local recovery does not use public ZIP transfer caps and never depends on live-body retention. Free-space checks reserve database work and metadata on clone-capable volumes, or the independently copied payload and metadata when cloning is unavailable. Same-volume immutable Linux source bodies add metadata rather than another payload allocation; restore still reserves independent materialization. A capture stays in the temporary namespace until its payload, manifest, and recovery descriptor are synced, then one directory rename publishes them together; an interrupted capture is reclaimed on the next locked startup, a matching retry reuses a published snapshot instead of copying again, and a pending point targeting another app version is detached before subsystem writers start.

A backup must identify the current schema or a recognized released schema with a complete upgrade route. Its manifest identity and extracted store are validated independently, only the staged copy is upgraded, and current shape and retained references are checked before live state is replaced. An imported `history-retention.json` is suspended in staging and its checksum updated, so another device's deletion policy cannot start removing local history without review; malformed retention data rejects the archive before any live replacement. A manifest is a claim, not the bytes. Product version travels with a backup as provenance and is not a gate: two builds with different product versions and one identical shape are equally restorable.

The archive always names its SQLite payload `store.db`. An internal restore marker maps that payload to the configured local database filename; archive metadata cannot choose the destination. Recovery pre-images preserve the configured database and its journals under canonical archive names. Native transfer journals are local operational data, excluded from exports and reclaimed on startup by process lock and recorded file identity without touching active transfers.

A failed apply is not a dead end. The marker records the failure, so the boot enters recovery mode and an explicit restore, recovery snapshot, or start fresh replaces the stuck transaction instead of being refused as a pending restart. The pre-image that transaction displaced is kept.

Pending rewind journals belong to the installation. Their durable references resolve
inside the restored checkpoint tree, independent of the original configuration
directory. Restore validates journal identity and required bytes before publication;
external project roots keep their explicit project identity.

Transcript export is a separate portable representation of one session and is not a backup of host state.

## Internal code

Internal packages that do not leak into a sticky surface can be reshaped directly: rename and update callers without an alias forwarder, delete dead implementations, and let tests and docs follow the intended design. The moment a shape crosses into the main database, OpenAPI, a project overlay, an extension schema, or agent-public vocabulary, use the matching class above.

## Invariants

- Every persistent artifact has an explicit compatibility class.
- Database baselines and wire synchronization move with their consumers.
- One artifact has one class even when several operations reference it.
- Rebuildable cache identity may outlive its evictable bytes; durable history keeps the bytes it names.
- Version fields keep distinct meanings.
- Breaking evolution is explicit, attributable, and recoverable.
