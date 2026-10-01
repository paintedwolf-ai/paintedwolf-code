# Scan findings

Every scanner result is normalized into one SARIF-aligned `SecurityFinding` model with stable identity, provenance, severity, location, and advisory metadata, so workflow gates, comparison, search, Den, and model summaries agree on what one finding means without understanding every engine's native output.

**See also:** [Scan supply chain](scan-supply-chain.md) · [Grounding](grounding.md) · [Tools](tools.md#scan-visibility-host-inject) · [Search](search.md)

**Machine truth:** `docs/openapi/components/schemas/scan/` · `lycaon/internal/scan` · scanner parser and gate catalogs

## Finding model

A finding carries a namespaced rule identity and message, a normalized severity, locations as its kind allows (source, package, container), optional structured data flow (source, sink, intermediate locations, nested calls), a primary fingerprint, scanner/tool provenance (`tool.driver_id`/`name`/`version`), kind and categories, an optional advisory or package coordinate, every engine that reported it after merge, and an ingest-time agent guidance code. The OpenAPI schema defines the exact nested fields.

A finding's own provenance stops at scanner identity. The run captures one immutable execution manifest (scanner and engine identity, parser/mapper, definition fingerprint, rule and exclusion digests, engine artifact identity, runtime policy, scope, fingerprint scheme); findings trace back to it through the run and finding-set ids rather than repeating it per row.

Opengrep traces keep the engine's call structure and source spans. Missing traces stay absent; nothing invents a flow. Before ingestion, every primary and trace location must resolve inside the snapshot and its line/byte-column range must fit the source; embedded projections remap all locations, including nested calls, before that check. Native source excerpts are not copied into traces. Den paginates evidence steps, JSON/CSV exports keep the structured flow, and SARIF reconstructs ordered thread-flow locations without inventing execution order.

## SARIF alignment

SARIF 2.1 is the interchange shape for results and full export. Native SARIF maps directly where possible; other parsers produce the same internal model, and export reconstructs a valid document. Host-specific kind, normalized level, advisory, scanner sources, and hint code ride in namespaced extension properties that standard readers can ignore. The parser validates bounds and required provenance before storage; malformed rows are attributed to their scanner run and never become partially trusted findings.

## Severity normalization

All gates and audiences use one closed level vocabulary:

```text
critical > high > medium > low > info; unknown is unscored
```

Parsers map their native level once at ingest. A canonical host extension level may override a coarse SARIF level when the producer declares it.

**An unrated SARIF result stays `unknown`.** SARIF treats a result without `level` as `warning`; the host does not, because that default is the format's, not the tool's judgment. An `unknown` finding stays visible in results, counts, and the histogram, but ranks below `info`: it maps to guidance severity `info`, so it does not meet the bundled gates' `block_on: [error]`, and it falls under the agent budget's `min_severity`. A tool that wants its results gated rates them.

`BuildSecurityFinding` (`internal/scan/findings/finding_build.go`) enriches an unrated finding from published facts, in order:

1. Published CVSS vectors on the record itself (`severity_source: osv.cvss`), a malware origin marker (`osv.malicious_package`, always `critical`), or declared database severity (`osv.database_specific.severity`);
2. folded twin records carrying a higher published severity via `MergeByAdvisory` (see [Advisory normalization](#advisory-normalization));
3. the advisory severity catalog (`internal/advisory/severity`), which indexes official CVSS vectors for CVE (`nvd.cvss`) and GHSA (`ghsa.cvss`) ids when ecosystem databases (Go vulndb, PYSEC), SAST rules with embedded CVEs, or unrated third-party SARIF omit them.

**The severity catalog is one bundled file.** [`config/runtime/scanners/advisory-severity.json`](../lycaon/config/runtime/scanners/advisory-severity.json) is embedded in the engine; each row is an upper-case `id`, the CVSS `type`, the `vector`, and its `source`. The engine derives every base score from the vector and refuses the whole catalog when a row repeats an id, carries an invalid vector, names a type its vector does not have, or holds an unknown field; a catalog that cannot load fails the scan that needs it rather than leaving findings unrated. [`scripts/build-advisory-severity.py`](../scripts/build-advisory-severity.py) maintains the file: `--cve` fetches a CVE from NVD, preferring NVD's Primary assessment over CNA scores. There is no runtime updater; a catalog change ships with the engine.

Severity is a property of the vulnerability, not of the record or tool that matched it. Ecosystem databases publish no score of their own, so several records for one vulnerability on one package fold into one finding carrying the best published severity. A finding with no published vector across any alias or rule id stays `unknown`, never an invented high. A record whose OpenSSF `malicious-packages-origins` field is present describes the package version itself as malicious: `kind: malicious_package`, level `critical`, `severity_source: osv.malicious_package`.

Severity expresses reported impact, not approval posture; detection-pack levels and authorization risk bands are separate vocabularies. Severity, evidence basis, and coverage completeness stay distinct: a rule reporting `ERROR` is not proof of exploitability, confidence is never inferred from message wording, and an LLM may explain a finding's evidence but cannot raise its authority.

## Fingerprint

`PrimaryFingerprint` (`internal/scan/findings/fingerprint.go`) derives the identity that persists across runs from driver id, finding kind, rule id, normalized location (URI and start line), and the advisory's canonical OSV id when present. Package name, version, and ecosystem do not participate; cross-engine advisory merge is a separate step. The scheme is named by `fingerprint_scheme`, currently `security-finding-v1`; changing identity semantics requires a new scheme, never a reinterpretation of stored hashes.

- Namespaced rule ids prevent cross-scanner collision; normalized relative URIs survive project moves.
- Identity comes only from what the scanner reported. Severity enrichment adds a level, never an identity, so a catalog change never re-keys a finding.
- A scanner-supplied fingerprint is accepted only under the parser contract.
- The fingerprint is deterministic for the captured source snapshot.

`scan_compare` requires equal fingerprint schemes and execution fingerprints. A scheme or scanner-input change is an incompatible comparison, not a mass regression.

## Advisory normalization

One vulnerability is published under several ids: a CVE, a GHSA, and one id per ecosystem database (`GO-`, `PYSEC-`, `RUSTSEC-`, `MAL-`, …), each listing the others as aliases. `internal/advisory` keys a dependency finding by the **canonical id** of the whole alias set: the first CVE in sorted order, else the first GHSA, else the first database id. `osv_id` carries the canonical id; `cve_ids`, `ghsa_ids`, and `aliases` carry every id by family.

Rows merge when they share the package coordinate (normalized ecosystem, name, version) and **any** advisory id: a Go vulndb record and its GHSA twin, or a CVE row from a second engine, become one finding re-keyed by the canonical id of the union, with the best level, its severity provenance, every location, and every engine in `sources`. The merged identity is a pure function of the group, not of scanner output order. An advisory id alone is not enough to merge: the same advisory can affect several packages or versions.

Advisory metadata keeps raw CVSS vectors with parsed scores, severity provenance, reference URLs, and published fixed-version boundaries for the matching package. A boundary is not a promise that one version is the right upgrade for every branch.

`scan_query` filters on `kind`, exact `advisory_id` (aliases included), and `fingerprint`. `view: groups` inventories rules or advisory/package coordinates before pagination, with stable group ids, complete stored occurrence counts, and bounded representative locations; `view: findings` drills down. Workflow reports budget groups before location samples, keep unsuccessful and empty executions, and distinguish raw totals, retained rows, represented occurrences, and listed samples. Assessment links use group ids; absent links mean unassessed. A review phase may declare `include_scan_inventory: true` to attach the run-bound dependency inventory to worker assignments.

## Parser contract

Every registered parser returns complete `SecurityFinding` rows. The selected scanner slot supplies trusted scanner identity, kind, and categories after parsing; the payload cannot impersonate another scanner. Parsers own bounded decode and schema validation, path normalization relative to the captured scope, severity and advisory mapping, fingerprint production or validation, warnings for dropped native fields, and stable error attribution. Exact mappings live in the scanner catalog and tests. File-backed reports that grow with repository size are decoded incrementally; byte caps belong on presentation projections, not on the raw report path that establishes coverage.

## Evidence binding

A scan run produces one evidence artifact bound to scanner id and driver, execution manifest and fingerprint, fingerprint scheme, declared scope, immutable source snapshot id, rule-set identity where applicable, typed coverage state and warnings, terminal run state, and the authoritative finding-set id. Findings reference the run rather than repeating the proof; an excerpt without its run cannot substantiate which bytes and rules were scanned.

## Audience projections

| Audience | Projection |
|---|---|
| Model prompt | bounded guidance summary and handles |
| Scan summary | counts, severity histogram, scope, caps, grouped analysis limitations |
| Scan query | paged full findings under supported filters |
| Project ledger | paged project-wide entries with state, history, absence facts, and the covering decision |
| Compare | new, resolved, and persisted fingerprint sets |
| Den Security | ledger rows and run rows, drill-down, filters, and export |
| Search | finding identity and searchable summary/provenance |
| SARIF export | complete compatible result document, decisions as suppressions |
| OpenVEX export | advisory judgements as VEX statements |

Raw engine count, normalized finding-set count, and injected guidance count are different facts. The full normalized set is durable and queryable; `agent_budget.max_guidance_findings` and the injection cap limit only the model-facing projection, and a budgeted summary is never described as "all findings".

## Scope honesty

Every run records how input was selected: host source floor, driver-selected source, dependency inventory, secret scope, container target, or custom declared scope. Scope descriptions are projected from the captured scope kind and exclusion facts; they never infer coverage from a scanner id or credit the host with filters an external driver applied. Installed dependency trees and source snapshots are distinct inputs, so their counts can differ without being inconsistent.

## Source scope

Nothing the host reads on its own initiative may cost more than the tree it admits. A project may hold millions of files under one folder (build output, a data set, a benchmark's sandboxes), and the scan plane keeps working there without assuming the folder is a repository. One classifier, `internal/sourcescope`, governs every host-initiated walk, and the walk primitive (`sandbox.SurveyWalk`) carries the budgets.

Capture, the publication of the source generation that scans and history bind to, decides admission **at the directory, before opening it**, from three layers in order:

1. **The excludes floor** (`scan-excludes.yaml`), never overridden.
2. **The project's declarations** in `.paintedwolf/source-scope.yaml`, applied only when the project's scan-config surface is trusted: `exclude` leaves a subtree out, `include` admits one an ignore file would have left out, and `budgets` replace the device's.
3. **Ignore files in the tree** (`.gitignore`, `.ignore`, `.git/info/exclude`), read with the gitignore grammar by the host itself. No tool runs and the tree need not be a repository.

**Traversal has no default repository-size ceiling.** The directory, subtree, and walk budgets in `config/runtime/source/scope.yaml` default to zero (unbounded). Capture streams wide directories in batches, yields shared I/O admission between files, and persists its manifest incrementally; publications serialize per root, and unrelated roots progress independently. A device overlay (`source-scope.yaml` in the engine config directory) or a trusted project may choose observation caps. A directory a budget refuses is never guessed at: it becomes a **boundary** in the generation (`source_snapshot_boundaries`) with its reason and what was read before the cut, the admission mode becomes `scope_bounded`, and every scan built on it reports `bounded` coverage: every admitted file was read, and what was cut is named. Bounded coverage still establishes a scanner's base, so the next generation scans as a delta, and two bounded runs compare when they share the same unobserved directories. A boundary an ignore file or the floor drew is policy and leaves the admission mode `scope`. An unreadable directory is an `unreadable` boundary and makes capture quality `observed`; an unreadable root fails capture.

Ignore files govern capture only. The catalog behind Files, code search, quick-open, and `summarize_catalog` reads them **as walk order, never as exclusion**; its [directory-priority catalog](../lycaon/config/runtime/source/directory-priority.yaml) also defers likely build output, dependencies, caches, and version-control metadata, and these patterns never become capture exclusions. Host tools that survey for an agent (`list_dir`, `grep`, `find`, `wc`, `stat`) and the write path read no ignore file at all. Each new scope reads current ignore-file bytes; parsed-pattern reuse requires matching content and the same root-relative domain.

## Source snapshots

Security runs and review evidence bind to a content-addressed source snapshot: a manifest that names each admitted file's content and copies no bytes. A survey asks git's index first, so a file clean against the index is identified by the blob id git already computed; a dirty or untracked file is read once for its digests and remembered by its stat facts; a file too large to hash is identified by stat facts alone. The first publication of a root surveys it; every later publication is the covered generation plus what the watcher reported changed. The tracker trusts itself no further than the watcher: a root without complete coverage, a root this process has not surveyed, a burst wider than it holds, or a change to an ignore file sends the next publication back to a streaming survey. Only changes inside the capture scope move a generation, so churn under an ignored or bounded directory cannot keep a publication from converging. When admitted writes keep landing, the publication stops after a few passes and is exact as of its last pass; a file that changed under the read, or a root no watcher covers, leaves the generation `observed`. The run records capture quality, admission mode, and boundaries so an observation cannot be presented as more than it is.

Watcher paths are invalidations, not scan inputs. The host publishes a new generation, diffs its manifest against the generation each scanner last covered, and derives exact upserts and deletions. A scanner without a base takes the generation as its base rather than scanning it. A disappeared watcher path therefore cannot trigger an `lstat`-based scanner failure or widen a scan to a parent directory.

A dispatch claims its scanner series with its own heartbeat (`claim_heartbeat_at`) for its lifetime. Preparation has no fixed elapsed-time ceiling; its owner can cancel it, and cancellation requeues unfinished claims. A claim whose dispatcher died is recovered when its heartbeat ages out. Every transition is logged under `component=scan_cadence` with each scanner's target named.

## Automatic scanning is incremental

Opening a project scans nothing. Attaching a root publishes its generation and records it as every selected scanner's covered base (`scan_series.last_covered_snapshot_id`, pinned against snapshot sweep). Each admitted write burst arms every scanner as a delta against its base, settled by `cadence.write_burst_settle_ms` and capped by `cadence.max_defer_ms` under continuous writes; a refresh after writes overtook a running scan, a file-count drift, or a burst too wide to carry as paths arms a delta too.

A full pass (every admitted file, in resumable chunks with `progress` on the record) starts only when asked: a person from the Security stage (`POST /v1/projects/{id}/scans`, trigger `manual`), an agent through `scan_pack` without paths (trigger `scan_pack`), or a workflow phase that declares `full: true` (trigger `phase_enter`). The pass is recorded when asked for (`security_full_passes`, with the sessions and workflow runs that asked). Its scanners read one generation and dispatch together: a scanner still finishing earlier work holds the pass until free, shown as waiting rather than absent. A request joins an unfinished pass that covers its scanners; any other request records a pass of its own that waits. A scanner deselected before the pass starts leaves it as not started. An execution identity that moved (rules, engine bytes) keeps the covered base: the delta still runs, its coverage reports that the full pass behind it no longer applies, and the next full pass restores authority.

Scheduling consumes the shared repository change feed and the snapshot tracker. A committed change wakes cadence to recompute its deadline; it does not invoke an engine. Writes during a running scan coalesce into one successor. Dispatch deadlines and claim leases are recovered from durable state at startup; `cadence.reconcile_interval_ms` and `runner.reconcile_interval_ms` (both 60 seconds) bound recovery after a missed notification, and `runner.retry_delay_ms` bounds retries after a service or publication failure. At completion, scheduling asks the snapshot store whether existing watcher observations demand a successor; this never walks source files, and strict consumers still use the physical currency check.

A delta reads each changed file twice: as the base held it and as it is now. Findings matched across the two by primary fingerprint, then by rule, file, and message for a line that merely moved, are what the change left alone; the rest is what it introduced or fixed, recorded on the scan as `delta` and on the series as history (`scan_finding_events`). The base half is answered from `scan_blob_findings`, keyed by execution identity, blob hash, and target path; complete current results populate this cache before old bytes become unavailable, including files with no findings. File-local warnings exclude their files; global warnings exclude the whole result; a finding that spans files excludes every participating file. Incomplete coverage disables delta comparison rather than inventing fixes. Every stored finding carries `history.introduced_at`; `scan_query` filters on `introduced_since` and returns `fixed_findings` for `fixed_since`. `GET /v1/projects/{id}/security` states the baseline, the last full pass, the pass running now, and each scanner's readiness.

Deltas never block anyone: no tool waits on one, no edit is held, no turn is gated. When a delta of the agent's own writes introduces or fixes a finding, the host informs every open session in the project with a `scan.delta` note at the next safe boundary; an agent that wants to wait asks for a pass with `scan_pack`. Library engines serve deltas from a resident worker that holds its compiled rules, exits after `scanworker.IdleExit` (5 minutes) of quiet, and respawns on demand, so a one-file delta costs an engine call rather than a process spawn. An engine that reads files one at a time gives each file `runner.file_timeout_ms`; a file over it is a `target_unscanned` warning, never a stalled run.

## Engines receive files, never a root

An engine never walks the tree. It reads the live root under the generation's file list: a full scan names every admitted file and a path-scoped scan names only the admitted files under its targets, so no copy of the tree is made. When the engine finishes, the run checks every file it was handed against the generation's stat facts and, when those differ, its content digest. An identical rewrite preserves coverage; a file whose content no longer matches is a `source_moved` warning, its findings describe neither the generation nor the tree, and coverage is `partial`. A delta's base versions are staged out of the revision store, git's object store, or the tree into a scratch directory and removed after; a base version nothing holds any more leaves the scan without a delta. Files reach an engine in chunks of `runner.chunk_files`; each finished chunk is kept under the scan's evidence root, so a scan a restart interrupts resumes at the chunk it was on. An external command scanner keeps its declared working directory.

Enqueue may create a visible warming run while publication completes; the scanner cannot claim the job until the final snapshot id and source facts are bound.

## The project ledger

A scan run answers what one engine saw in one input. The ledger answers what the project has open and what has left, folded across every scanner the project selects: one row per fingerprint per scanner (`scan_finding_ledger`), a rebuildable projection written in the transaction that writes `scan_finding_events`, so history and the surface that reads it cannot disagree. The events remain the authority.

Every completed scan records history. A delta reports its comparison against its base; a scan without one is compared with what the series already holds open, scoped to the paths it named, so a finding a path-scoped scan never looked at is not one it fixed. A finding whose history was never written is one the ledger cannot date; missing history is never read as "old".

A scan's `delta` reports `status: complete` with `counts`, or `status: unavailable` with an `unavailable_reason` and any `unavailable_paths`. Base findings are reused only for the same execution fingerprint, content identity, and path. On a cache miss, comparison reads verified retained, Git, or unchanged live bytes. An unretained old version makes the comparison unavailable; it does not imply zero old findings or reduce current scan coverage. Snapshot manifests identify files without copying the tree, so this is an expected limit when a file changes before its base was scanned.

The ledger stores three states and derives the rest at read time in the `scan_finding_ledger_view` view (`schema.sql`, column `ledger_state`), which the row page, facet counts, and level histogram all read:

| State | What it means |
|---|---|
| `open` | the scanner's authoritative set still reports it |
| `reopened` | the series recorded it fixed, then observed it again |
| `fixed` | it left under complete coverage and an unchanged execution identity, or a path-scoped scan re-read its file under complete or bounded coverage |
| `not_observed` | it left under bounded or partial coverage, so its absence establishes nothing |
| `unverified` | it left while the scanner's execution identity was moving, so the pass behind its absence no longer applies |
| `ignored` | open or reopened, and an unexpired ignore entry covers it |

The absence facts (`left_target_kind`, `left_coverage`, `left_execution`) are stored; the verdict is not, because the execution half is only answerable against the identity the series runs now.

`FindingLedgerCounts` totals the project independent of a query's filters, so a narrowed view never reads as the project's total. Fixed, not observed, and unverified stay separate columns: folding them into one "resolved" number is how a scan gap becomes a clean bill of health.

`POST /v1/projects/{id}/findings/query` pages the ledger. Filtering, ordering, and paging happen in the store. The join to `scan_series` scopes the view to the scanners a project currently selects, so deselecting one withdraws its claims without destroying its history. Each entry carries `last_scan_id`, the run that recorded its last event, which a reader drills into through `scan_query`.

## Ignoring a finding

A project's scanner decisions live in the `findings` section of `.paintedwolf/ignores.yaml` ([Project overlay](project-overlay.md#ignoresyaml)). An entry is a conjunction of optional predicates (`path`, `kind`, `scanner`, `rule`, `advisory`, `fingerprint`), and a finding is ignored when it matches all the ones present. Wherever the host owns the fact, one entry covers every engine that reports the same thing. Engines still honour their own ignore files first; this is the layer above them, and the one a person can read.

The decision is a display fact, never an ingest filter. An ignored finding is still scanned, still recorded, and still in the scan's authoritative set; if it were dropped, the next comparison would read the decision as a fix. What narrows is the agent's view: guidance, the finding budget, and the run's verdict are computed over the findings no decision covers.

`ignored` is derived at read time and displaces only the presence states: an ignored finding that later leaves is described by its absence like any other. The ledger caches each row's decision in its `ignore_*` columns and re-applies the catalog whenever the file's digest moves, so an entry added by hand or arriving in a pull takes effect on the next read.

`reason` is required. `expires` is a calendar day, exclusive; a lapsed entry stays in the file, stops applying, and its findings return to the open list. `justification` carries an OpenVEX `not_affected` justification and is accepted only on an entry naming an advisory.

`GET|POST /v1/projects/{id}/findings/ignores` and `DELETE /v1/projects/{id}/findings/ignores/{entry_id}` read and edit the file through the YAML node tree, so comments a person wrote survive an edit from the stage. The listing reports how many ledger rows each entry covers now, so an entry matching nothing is visible.

These are distinct from `FindingDisposition`, the triage value a scanner's own report supplied; an engine's judgement and a person's never land in the same field.

Active entries in the same file's `secrets` section classify exact public fixture values. When every value behind a secret finding is covered by an active declaration, it becomes ignored with the fixture's reason; removal, expiry, or disabled project trust restores normal classification without a rescan, and a changed value at the same location does not inherit the exception. Background scans whose root is shared by several projects do not apply a project-specific exception without unambiguous attribution. Secret values and private fingerprints are excluded from exports. See [Ignored public values](secrets.md#ignored-public-values).

## Exporting decisions

`POST /v1/projects/{id}/findings/export` writes what a ledger query matches, with the query run in the store rather than over a page a client holds. `sarif` carries the matched findings as SARIF 2.1 results, each project decision attached as a `suppressions[]` entry of kind `external`. `openvex` carries the advisory judgements: an entry with a VEX justification becomes `not_affected`, an advisory still reported becomes `affected`, and one a scanner stopped reporting under coverage that established something becomes `fixed`. `not_observed` and `unverified` absences are left out rather than published as good news.

## Security stage lifecycle

The stage opens on the project's findings. **Open** is what the scanners report that nobody has decided about; **All** is the whole history, including what left and what the project chose to ignore. A run stays reachable as the evidence behind a row.

An empty list states only what scans have reported. Until a full scan completes with complete coverage, the empty line also says that only changed files have been scanned, or that the last full scan did not cover the whole project.

A finding's detail and the selected-row action bar offer **Add to chat**, which attaches the selected findings (rules, severity, locations, fingerprints, evidence, ledger state, scan identity) to one destination chat without sending. Selected rows also offer **Fix with agent**, which stages a draft rather than sending one, and **Ignore**, which offers only the predicates every selected finding shares, requires a reason, defaults to an expiry, and shows the YAML before it lands.

Queued and running scans show status and chunk progress; unsuccessful terminal runs show their outcome and recorded error. Only completed runs enable findings queries and exports. Status and results have separate loading and error states, and result caches are scoped to the selected run. Chunk progress commits the run, its summary, and its SSE event together; the selected run also polls until terminal so completion arrives even when an event is missed. A completed full pass keeps its scanner outcomes visible, and a failed scanner makes established pass coverage at most partial. A pass that finishes within the minimum visible interval stays at its final state for the rest of it ([Den](den.md#presentation-and-refresh)).

All findings are visible by default. "New since" is an explicit filter on recorded introduction history and loads current and fixed findings together. A missing coverage response is shown as unknown, never as proof that scanning has not started; bounded and partial coverage copy does not invent a cause.

## Agent summary

The summary includes raw count, authoritative finding-set count, counts by level, applied guidance budget, human-readable scope, source and coverage status, grouped analysis limitations, and supported query filters. Guidance rows carry stable finding handles and the minimum location/message needed to choose a deeper query. When rows are omitted, the summary states the omission count and offers `scan_query`; it never truncates silently.

`warning_summary` groups distinct diagnostics by kind with occurrence, file, and rule counts. Default receipts, board orientation, and worker digests use the aggregate; full scan views keep the rows. Den exposes limitations through a neutral "Analysis issues" item in the scan chrome, loaded on request; routine limitations create no notification and displace no findings. An empty partial or bounded run says "No findings in analyzed code", and the bounded popover names the source budget rather than an engine gap. Grouping never changes coverage authority: incomplete analysis cannot become a clean gate. Opengrep limitations (unavailable callback bodies, bounded alias or recursive-call analysis, unsupported value operations, lost framework model identity) describe uncertainty in the analysis; they are not findings and do not raise severity.

Default host injection gets one compact scan line: terminal state, source revision, severity tail, staleness, and at most a few hottest locations. Full per-engine detail is available through the board or tool on demand.

## Process and result budgets

Scanner execution uses weighted shared CPU admission with one unit reserved for interactive work, below-normal process priority where supported, bounded concurrency, and per-engine job limits. CPU-heavy library engines run in managed child processes so their allocation and cancellation do not contend inside the sidecar.

The bundled Opengrep invocation disables its implicit per-file size filter and per-rule timer; declared scope exclusions still apply, and large source files remain eligible. Large explicit target lists run in bounded process-argument batches; a failed or canceled batch invalidates the run instead of admitting earlier batches as coverage.

Each run captures its scanner runtime policy. The soft limit marks the run `long_running` without discarding work. The bundled policy has no overall deadline (`hard_limit_sec: 0`), so repository size does not determine whether a scan can finish; a user-selected positive hard limit ends it as `timed_out`. A result remains bound to the snapshot it scanned even if a newer generation is already waiting. Large native result bodies spill to project host data; Den and agents use summary/query/compare instead of loading native output.

## Comparison

`scan_compare` diffs two scans' findings by primary fingerprint, with an advisory OSV id fallback for SCA rows: new findings require attention, resolved findings are evidence of change rather than proof of correctness, and persisted findings remain visible with updated provenance. Comparison refuses missing, incomplete, cross-project, self, cross-scanner, execution-incompatible, or scheme-incompatible pairs, and reads authoritative finding sets, so an incremental run is compared after its declared replacements and deletions are reduced against a compatible base. The compare contract is in [Scan supply chain](scan-supply-chain.md#scan_compare-verify-fix-diff).

## Invariants

- One complete finding model serves every scanner.
- Provenance binds each result to scanner definition and source snapshot.
- Every completed scan records finding history for the scope it can speak for.
- A finding's absence is never reported as a fix unless a scan re-read it under coverage that establishes one.
- An ignore never changes what a scanner reports, and never removes a finding from the set the next comparison reads.
- Gates use normalized severity, never raw labels.
- Fingerprints are deterministic for a captured snapshot and versioned by scheme.
- Audience projections state caps and omissions without truncating durable findings.
- Scanner payloads cannot choose trusted scanner identity or scope.
