# 1.0.2 release readiness review

Reviewed on 2026-10-10 against aggregate candidate `e2ec2693c523860c50868ef5ef0eb54960d4245a`, retained as `review/release-candidate-baseline`. That aggregate includes open work; it is not the current landing commit. Fix PRs use that base to keep their diffs reviewable. Assemble and qualify the intended landing state separately.

## Corrections

| Item | Independent finding and remedy |
| --- | --- |
| Signed bundle configuration | The pinned Tauri CLI requires the updater public key for signed artifacts. [#475](https://github.com/paintedwolf-ai/paintedwolf-code/pull/475) derives the signing-generation bundler configuration from the key registry. |
| Feed publication credentials | The publication environment retains the old unnumbered secrets; `FEED_SIGNING_KEYS_JSON` is absent from the visible repository/environment inventories. Organization inventory was inaccessible. [#476](https://github.com/paintedwolf-ai/paintedwolf-code/pull/476) proves every retained generation before publication and halt writes. Provisioning remains an operator action. |
| HTTPS transport | Reqwest disables defaults and lacks a TLS feature after removal of the updater plugin. [#477](https://github.com/paintedwolf-ai/paintedwolf-code/pull/477) enables Rustls and adds a bounded TLS-handshake regression. Backup transfer and startup confirmation currently use loopback HTTP, so the supplied report overstated their exposure. |
| Recovery rejection contract | Malformed requests shared the stale code and prose reason. [#478](https://github.com/paintedwolf-ai/paintedwolf-code/pull/478) separates invalid requests and typed stale reasons from diagnostic explanation. |
| Namespace migration | The released fixture did exercise file-head conversion, but omitted directory and branch cases. [#479](https://github.com/paintedwolf-ai/paintedwolf-code/pull/479) adds nested/deleted/unresolved/worker cases and bidirectional comparison before dropping the old table. Noncanonical paths refuse without mutation. Scratch is explicitly zero because the reconstruction uses the main database reservation. |
| Expired Trash history | An unavailable newest receipt blocks older history. [#480](https://github.com/paintedwolf-ai/paintedwolf-code/pull/480) retains an unavailable entry, selects other actions, qualifies the confirmation copy, versions the new receipt and clears it before another move. Receipt identity encoding belongs to that version. Existing operation-plan JSON remains governed by the database schema and migration contract. |
| Blueprint relocation | Fresh sqlc generation exposed an omitted review revision and incompatible generated return type. [#481](https://github.com/paintedwolf-ai/paintedwolf-code/pull/481) retains the revision in the query and event payload. |
| Subtree lookup | The view prefix query resolves ancestry for unrelated heads. [#482](https://github.com/paintedwolf-ai/paintedwolf-code/pull/482) first selects indexed directory descendants and entries. |
| Symbol retries | Automatic `symbol_pending` polling is unbounded. [#483](https://github.com/paintedwolf-ai/paintedwolf-code/pull/483) caps retries for one query and resets on a new search. |
| Bugbash pinning | Closeout retries changed under 1.0.0. [#484](https://github.com/paintedwolf-ai/paintedwolf-code/pull/484) seals the shipped version, bumps live bugbash to 1.1.0 and checks retained archives in release preflight. |
| Failed operation scope | Non-delete effect errors can leave a prepared row suppressing observation. [#485](https://github.com/paintedwolf-ai/paintedwolf-code/pull/485) releases the scope when input facts prove no publication; partial and uninspectable effects retain recovery protection. |
| Completion and wire metadata | [#486](https://github.com/paintedwolf-ai/paintedwolf-code/pull/486) declares the closed completion vocabulary and compares against the exact v1.0.1 OpenAPI bundle. |
| Retired rule facts | [#487](https://github.com/paintedwolf-ai/paintedwolf-code/pull/487) publishes retirement/successor notes for the three removed facts. No aliases restore their old meanings. |
| Shutdown expiry drain | Timer shutdown did not drain an already-running expiry callback. [#488](https://github.com/paintedwolf-ai/paintedwolf-code/pull/488) waits for callbacks before persistence cleanup under the shutdown deadline. |

## Findings without a code change

- The v1.0.0 and v1.0.1 SQL baselines are byte-identical. The registry can represent both with one released shape.
- Migration execution checks source shape, step checksum, target shape and foreign keys in one transaction. Application startup captures verified recovery evidence before a live upgrade. Unknown or newer shapes refuse admission.
- The candidate OpenAPI operation set matches v1.0.1. Comparison found no removed enum values or added required fields in existing wire schemas. `/v1` authentication and constant-time token comparison remain intact; non-loopback binding still requires explicit opt-in.
- The original artifact trust key and old-client feed format remain available. Feed signatures are verified before parsing; artifact signatures precede extraction and platform signature verification. Extraction rejects traversal.
- Signed feed monotonicity/replay checks remain necessary. A fixed maximum age would also reject a legitimate long-lived latest release and strand infrequent clients, so no blanket expiry was added.
- Update records contain no secrets and live inside private directories. Default record-file modes do not justify a separate migration.
- Approval and grant semantics were reviewed separately from confinement refactoring. Grant packages are unchanged. Confinement derives defaults and exact grants from structured facts, preserves control-plane/key-material floors, rejects ambient home/root write authority, and narrows mediated proxy endpoints to bound loopback leases.
- Backup restore validates and upgrades staged stores before publication. Credentials remain outside backup inclusion. Recovery directories/files use private modes. Source history and workflow pins are retained; missing workflow versions use structured refusal.
- Native file operations preserve content at the selected source, destination, retained recovery or OS Trash. Losing the native acknowledgement can still require manual recovery. Unit/source review cannot certify OS behavior under power loss or every supported platform.
- Rendered Markdown already has a substantial script/event/URI/embedded-content sanitization suite. The DOMPurify major bump needs that suite on the assembled commit and packaged rendering qualification; an unperformed spot check is not itself a defect.
- License inventory is generated fail-closed from Go, npm, Rust and bundled runtimes in the managed checks and bundle build. The assembled TLS dependency graph needs a fresh generated inventory and passing qualification.

## Release blockers and evidence limits

Provision the feed key map and run its credential check. Build signed packages using the corrected bundler configuration. Qualify the exact assembled release commit, including the immutable released-store rehearsal and refreshed editable 1.0.2 fixture. Exercise a Preview RC installed from the signed package through feed check, download, verification, quit installation, startup confirmation and recovery. Its updater must successfully discover a later Preview version; an unsigned local handshake alone cannot prove this.

Confirm migration-refusal UI preserves the store and names recovery evidence. Rehearse rewind/checkpoint and pinned-workflow behavior across the upgrade, clean shutdown while work is draining, and per-platform Trash behavior. Feed withdrawal cannot downgrade a revision-2 store: migrated users need a compatible fix-forward release, or explicit snapshot recovery with subsequent changes discarded.

Scoped local checks and ready PRs are evidence for their own sources only. They do not establish combined landing-state correctness, signing, Gatekeeper, translocation, crash/power-loss behavior, or release publication credentials. No release was published as part of this review.
