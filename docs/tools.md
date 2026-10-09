# Tools

Tools are structured capabilities published with argument schema, execution policy, a subsystem owner, lifecycle, and evidence behavior. The model proposes calls; the host validates them, and the selected owner states the outcome of an admitted execution.

**See also:** [Architecture](architecture.md#subsystem-owners) · [Adding a tool](adding-tools.md) · [Coordination](coordination.md) · [Grounding](grounding.md) · [Security](security.md) · [Authorization](authorization.md)

**Machine truth:** native manifest `native-tools.yaml`, per-tool `schemas/`, `profiles/`, `tool-command-equivalence.yaml`, and `tool-presentation.yaml` under `lycaon/config/packs/painted-wolf/platform/tools/` · non-native catalog allowlist `lycaon-tools.yaml` beside them · generated invocation contracts in `internal/toolcontract`

---

## Altitude

The preferred tool is the highest-level capability that completely expresses the action.

| Altitude | Use |
|----------|-----|
| Domain tool | Files, git, scans, search, workflow, delegation, browser, and other host operations |
| Native analysis tool | A bounded first-party read or transformation with structured arguments, rejects, and evidence |
| Confined MCP | An external protocol tool whose complete effect remains inside host confinement and policy |
| Generic `command` | Last resort when no structured capability expresses the operation |

Higher-level tools expose intent and consequences as fields, so the host can enforce scope, choose approval, preserve evidence, and offer specific recovery without parsing shell text. The generic command path still takes structured argv, not a shell string, and is not a shortcut around a native operation, project boundary, or approval gate.

### Root-scope guards

Native tools do not extend past attached project folders without explicit approval. Project file tools resolve through attached root identity, the shared default filesystem boundary, or approved absolute paths. Paths are normalized, symlink escapes are refused, and write scope is checked at the final mutation boundary. An absolute path outside attached roots and permitted scratch areas raises an approval card at every posture, and the approved grant admits the call ([grant shapes](authorization.md#the-filesystem-axis)). Without a covering grant, after the person declines, discovery tools report `SURVEY_PATH_ESCAPE`.

Temporary, cache, and tool data paths derive from the same OS conventions as subprocess write roots; below Strict, native tools need no extra outside-folders approval there. Strict reviews a native write to those paths and leaves reads and subprocess write roots alone. Scratch files stay outside project identity, editor presence, and rewind; deliverables belong in attached folders so they show in Files.

Multi-root calls use explicit root addressing when a relative path is ambiguous. Host-data-relative spill paths are a separate jailed namespace that never exposes the absolute config directory.

`@scratch/<path>` addresses the invoking session's private scratch folder, a host namespace no attached root may share. One grammar resolves it for native tools, `cwd`, stream redirects, and unquoted `command` and `verify` arguments, which reach the program as absolute paths; a quoted `@scratch/...` stays literal. Processes also receive the folder as `SCRATCH_DIR`. A worker's scratch is its own, so a worker returns findings in its result rather than as a scratch path.

Recovery reads and structured queries of retained tool output use the session's spill byte bound (64 MiB by default), including decompression. Ordinary project files keep the 8 MiB read bound (`read`, `summarize`, and `diff`; `grep` searches the leading 8 MiB) and the 20 MiB `jq` input bound. Native edits (`edit`, `replace_lines`, `code_rewrite`, `jq_edit`, `write`, and `restore_version`) have a lower 4 MiB bound, the size that version history and pre-turn checkpoints retain in full: a file or open document above it, or new content that would exceed it, rejects with `EDIT_FILE_TOO_LARGE` before any review or write, so every state a native edit replaces stays restorable. Removal and replacement follow the same promise without the bound: `delete`, `move` onto an existing file, and a `copy`, redirect, or extracted entry overwriting a file above it set the old entry aside in lifecycle recovery first ([Files live](files-live.md)). `command` has no such bound. The host's resolved path classification selects the bound; a similarly named project directory cannot widen it. Line and output limits still bound the returned page. A single long JSON string can be recovered in bounded substrings with `jq`.

Scope is rechecked where the effect occurs. A model-visible schema, prompt instruction, or earlier path classification never authorizes a later write.

---

## Tool lifecycle

Dispatch selects one complete definition (metadata, schema, handler, contract, presentation), freezes it with the structured arguments, and opens a receipt before generic pre-invoke checks. An admitted call crosses exactly one [subsystem-owner boundary](architecture.md#subsystem-owners), and the runtime settles the receipt exactly once. Unknown or off-surface names select no definition and create no receipt; once a definition is selected, every path settles it, including a pre-invoke refusal with `invoked: false`.

| Outcome | Meaning |
|---------|---------|
| Completed | The owner ran and stated a successful outcome and evidence |
| Rejected | A structured policy or contract refusal prevented the effect |
| Failed | The owner ran but could not complete its operation |
| Interrupted | The process or host boundary ended before settlement |

The ledger records only settlements that satisfy the receipt rules. A settlement it refuses is a host fault, not an outcome of the call. The receipt settles as failed with class `host_fault` and code `INVOCATION_SETTLEMENT_REFUSED`, keeping the owner's `invoked` fact. The call's result states that code in place of the owner's output, which never crossed result delivery. The batch's remaining calls settle as `TOOL_BATCH_NOT_RUN`, and the turn ends with the [`host_fault`](den-notices.md#host_error-the-session-notice-roster) notice, so no tool call is left without a result. A ledger that cannot write at all ends the turn the same way. That receipt stays running until the session stops or the engine restarts, and either settles it as interrupted. A host fault is never counted as the caller's repetition.

Result prose is presentation. Outcome, reject codes, process handles, source verdicts, owner references, and evidence ride typed receipt and result fields, so compaction or truncation cannot change their meaning.

## Held calls

A read-only tool that blocks on shared infrastructure must not hold the coordinator, and nobody can say in advance how long a walk, a search, or a repository read will take. The manifest lists such tools under `detach_after_budget`, and their call returns in one of two ways:

- It settles inside the foreground wait (30 seconds, like a command job) and returns its result as any call does.
- It does not. The call returns `{"running": true, "handle": "held-1", "tool": "find", "waited_ms": 30000}` and keeps running under a context the turn does not own. The turn continues.

The receipt settles at that handoff, as a promoted command's does. The handle joins the background-handle vocabulary: it rides `tool_result.process`, appears in the session's handle list and process topic, and Den shows it running until the topic reports it settled. `wait(until_complete=true, conditions=[{"kind":"process_done","handles":["held-1"]}])` subscribes to it; settling wakes only a wait that named it.

| Tool | Use |
|------|-----|
| `held_result` | State of a held call; once settled, its original outcome, codes, and result. `wait_ms` waits up to 30 seconds for a running call, which is how a profile without `wait` follows one |
| `held_stop` | Cancels a running held call. `stop_requested` acknowledges the request; the call settles with what its tool returns once it observes the cancellation |

A held call lives in memory, as a process handle does: a host restart ends it and its handle reads as unavailable. A session holds at most four running calls (`HELD_CALL_CAP_REACHED`), an identical call already running returns its handle instead of starting again (`HELD_CALL_DUPLICATE_RUNNING`), and the last sixteen settled results stay readable. The live-handle ledger injected each turn lists held calls beside command jobs.

Only `read_only` tools may be listed: an effect attempt or a journaled mutation must settle inside the turn that recorded it, and manifest validation refuses the listing otherwise. Canceling the turn cancels a call still in the foreground; only a call already held survives it, until the session is disposed.

## Native catalog

`native-tools.yaml` is the complete source for tool identity, description, schema reference, subsystem owner, lifecycle, approval reversibility, batch policy, per-tool concurrency limit, turn order, evidence kind, and profile membership. The owner is a stable subsystem category; `owner_ref` may narrow one result to a concrete job, process, or artifact without creating a new owner type.

This page does not reproduce the roster. The effective catalog changes through generated stock content, extensions, enabled MCP providers, and trusted project configuration, so a hand-maintained list would be stale by construction. Use the manifest and schemas, the generated contracts, the runtime tool list for the current session and profile, and `./task codegen:native-tool-contracts` plus its drift check after catalog edits.

Catalog-backed registration fails when metadata, schema, contract, and handler do not close over the same name. Dynamic MCP definitions publish one immutable generation so approval and dispatch see the same sanitized tool.

### Sibling execution

One assistant response may carry multiple tool calls. Every tool declares one `batch_policy` from a closed set, and that class decides which consecutive calls may execute together:

| Class | Sibling rule |
|-------|--------------|
| `serial` | Never concurrent: the posture for anything that mutates, spawns, or holds a resource |
| `shared` | Two **different** `shared` tools may run in one concurrent group: the bounded read and query surface (`read`, `grep`, `find`, `stat`, `git_log`, `recall`, `survey_repo`, and their peers) |
| `same_tool` | Concurrent only with further calls to the same name |

On top of the class a tool may declare a boolean opt-in argument, a per-tool ceiling under the global host cap, and an argument whose presence re-serializes the call even after an opt-in. `web_search` and `fetch_url` are `same_tool` behind an explicit `parallel: true`, capped at four; omitting the argument keeps them serial, and `fetch_url` re-serializes whenever `dest` is present, because a landed file is an effect. `summarize` is `same_tool` with no opt-in argument, so its siblings run concurrently by default (capped at four), the only tool that does.

Calls keep separate receipts, policy checks, results, and evidence even when their execution overlaps.

`scan_pack` keeps its immediate enqueue default. Without `paths` it asks for a full pass of every admitted file by the selected scanners, the only way an agent starts one, and joins an unfinished pass that covers them rather than starting another. The receipt carries the pass's `pass_id`; a scanner still finishing earlier work reads as pending with its `pass_phase` and no `scan_id` yet, and `scan_summary` with `pass_id` reads the pass at any point. `completion: summary` waits inside the same call for the whole pass; `timeout_ms` bounds that wait, and a timeout returns the pass as it stands with each started scan's `progress` while the durable jobs continue. `scan_query` narrows to what recent edits did with `introduced_since` and, with `fixed_since`, what they fixed. A delta the host ran on the agent's own writes never blocks a tool; its outcome arrives as a host note.

## Tool vocabulary

Tools are grouped by responsibility rather than by implementing package:

| Family | Responsibility |
|--------|----------------|
| Source | Read, search, summarize, and mutate project or host-data files |
| Git | Inspect and change repository state through one hardened git boundary |
| Verify and scan | Produce current source or security evidence |
| Command/process | Run bounded argv and manage durable or interactive handles |
| Workflow | Query or transition the active run and submit verdicts |
| Coordination | Dispatch workers, record findings, manage overlays, and update progress |
| Research | Search and fetch external sources under web-research policy |
| Secrets | Generate, recover, and revoke value-free references to host-protected random credentials |
| Page/visual | Drive live surfaces, measure them, and produce durable artifacts |
| MCP | Invoke an admitted external definition through its MCP subsystem owner |
| Utility | Parse, query, or transform structured data without widening authority |

The tool name is agent-public vocabulary. Retire or replace meanings according to [Compatibility](compatibility.md); never silently reuse a name for different behavior.

### Managed secret references

`secret_generate` returns a `{{paintedwolf-secret:…}}` token rather than credential bytes. `secret_list` recovers visible references after compaction or in a later project task, with value-free lifecycle facts beside them (stored version, substitution count), and `secret_revoke` permanently disables one. Retired bytes remain protected screening evidence until project deletion. Adding, relabelling, rotating, restoring, and native-authenticated reveal are human actions in project settings, not agent tools. Agent tools never expose a value, fragment, hash, or derived length. The complete lifecycle is [Secrets and redaction](secrets.md).

The token is a first-class string substitution in tools whose contract declares a `secret_reference_surface`: command and terminal input, `http_request`, MCP, and file mutation tools (`write`, `edit`, `replace_lines`, `jq_edit`) ([surfaces](secrets.md#value-metadata-and-reference)). File tools use slot-level resolution (`secret_reference_args`) and support `resolve_secret_references: false` for literal token emission in tests and documentation. Substitution captures an immutable deep copy after structural validation and before composed capability review. Policy and review retain references; only the executing subsystem and secret screens receive the resolved copy, after approval or silent policy release. Other tools carry the token as literal text. Only complete namespaced tokens with a canonical UUID are recognized; partial markers, examples, and object keys remain literal. Each original string is substituted once; inserted protected bytes are opaque.

For service setup, command, verify, and terminal calls may include `secret_use: {"services": ["http://127.0.0.1:8080"]}` alongside the managed reference. This requests one permission for the process handoff and subsequent `http_request` use at the listed origins; known secret permission joins socket, direct-network, and local-network reviews. Origins are declared recipients, never observations of what a process does. The task permission covers repeated use of the same values at the reviewed receivers; another scheme, host, port, surface, or value needs its own permission. See [service reuse](secrets.md#reusing-a-secret-with-a-service).

Every mutation-capable profile can reach all three lifecycle tools; requesting generation loads only the selected schema. That includes file- and plan-writing profiles, which may create or carry a reference for a later step even when their own surface has no resolving outbound tool. Read-only and research-only profiles deny the lifecycle tools.

### Bundled profiles

A tool profile is a declared capability surface for an agent role: allowed tools, filesystem scope, command policy, loop and output budgets, and optional deferred tools. Profiles answer which capabilities an agent may request; the effect boundary still answers whether a particular request may proceed.

Open-world `tools: all` means all effective registered tools admitted to that workflow surface, including enabled extensions and MCP definitions. Restricted profiles remain closed and do not gain a tool merely because it registered at runtime.

### Turn tool plans

Each model call compiles one typed tool plan from the active surface, profile, attached roots, live resources, session activations, web-research state, and current MCP definitions. Every consumer for that call reads the same plan: the provider receives its immediate schemas, invocation reachability sees its complete addressable set, and `request_tools` sees its deferred set. A named surface without a successful compile is closed, not an unrestricted turn.

Worker plans start from the profile-filtered capabilities rather than the coordinator surface catalog. In a profile, `sticky` exposes a schema immediately and `true` allows discovery through `request_tools`. Optional browser, research, scan, and administrative schemas are deferred in implementation and repository-research profiles; core editing, navigation, and lifecycle reporting stay immediate. Activating a tool loads only the selected schema; live-resource facts promote existing controls without admitting a new capability, and a prior activation cannot resurrect a tool removed from the current plan.

Invocation is bound to the schemas actually offered on that request. An unoffered call receives `TOOL_NOT_OFFERED` without running the handler; the call and rejection stay in the transcript so the model can load the deferred schema and retry.

On a surface, `floor` is offered on every call and `loadable` names the tools the turn may add. A loadable tool is immediate when the session's load ledger holds it (predicted by the turn decision, asked for through `request_tools`, or already used in the chat) and requestable otherwise. `request_tools` takes a description of the need: names the text spells out load directly, the decision engine ranks the rest of the loadable set against the text, and an unavailable ranker returns a paginated catalog of available names and descriptions. The model can select an exact name without ranking. Fallback results provide `discovery.next_need` to pass unchanged as `need` for the next page; healthy calls and standing schemas carry no discovery instructions. When the engine answers but no tool reaches `request.load_at`, its `nearest_loads` closest tools load anyway and the result lists them under `nearest`, so an answered request is never a dead end. Surface placement takes precedence over a profile's default deferral, while confinement, approval, and the profile's allowed-tool policy still apply at execution. Enabled MCP tools are deferred by default in the compiled plan and load as needed through `request_tools`, unless the provider is explicitly configured to always load. Catalog loading rejects ambiguous placement, duplicate or empty names, and loadable sets on surfaces that do not carry `request_tools` on the floor. Resource families expand the addressable surface before the plan is consumed, but requesting one member does not activate the others. A tool's declared `companions` in `native-tools.yaml` do load with it when a request selects it, bounded by the surface: the file-mutation tools declare one another, because a need to create or change files ranks them near one another and a turn that does one soon does the rest. Live resources supply their required controls through structured resource facts. The decision engine, its data, and its offline evaluation live under [`scripts/bialy/`](../scripts/bialy/README.md).

### Session scope

Some tools are meaningful only on the session the human addressed: a human-facing mid-turn note, workflow proposal, or root progress update must not appear on a spawned worker's private conversation. The manifest's `session_scope` declares whether a tool is available to the addressed root, a worker child, or both, and the host enforces it independently of tool names or prompt copy.

### Recall

`recall` is the agent-facing lookup over the evidence ledger described in [Search § Recall](search.md#recall): what a session and its worker legs already observed, returned as bounded, citable records rather than a re-read of the tree. Every bundled profile that can observe anything carries it as a sticky tool.

Three situations call for it, and the host names the tool at each one so the pointer arrives with the loss:

| Situation | Where the host points at it | Query shape |
|-----------|-----------------------------|-------------|
| A finished worker leg's envelope omits a detail the leg saw | the leg-finished kick, which carries the leg id | `leg:<id> <term>` or `handle:<handle>` |
| Compaction replaced a row, or shrank a tool result in place | the continuation record's reacquire section; the `[compacted …]` banner on the shrunk row | `handle:<handle>` |
| An earlier session on this project may already have looked | the coordinator's tool teaching | any query with `widen: project` (or `all`) |

A tool result's index row carries the evidence handle minted with it (`command#2`, `read#1`) as `handle` and the producing tool's name as `tool`, so `handle:<h>` from a banner resolves to the row and its recorded body, and `tool:<name>` narrows by producer.

Scope derives from session topology, never from the agent's claim about itself. A worker sees its own leg only and may not widen (`Code: RECALL_SCOPE_NOT_PERMITTED`); a root session sees its whole tree, finished and archived legs included, and may widen to the project or every attached project. The prompt teaches widening only where it is reachable.

Every result states a `resolution` the caller branches on instead of counting rows: `matched`; `no_match_in_scope` (this scope observed things, not that); `scope_empty` (nothing observed here yet: dispatch or observe, do not search again); `record_deleted` (it existed and the content is gone); `executor_degraded` (a partial answer, never evidence of absence). Hits carry `observed_at` and `currency`; a `changed` hit describes the file as it was. A `kind:code` or `kind:file` query wants the working tree and is rejected with `Code: RECALL_LIVE_CODE_NOT_IN_SCOPE`; that is `grep` or `read`.

### Audience and reachable remedies

A reject recommends only actions the receiving profile can perform: a worker without project write authority is not told to save a report, and a read-only agent is not told to repair a file. Remedy copy is rendered from registered facts and the current tool surface; structured reject codes remain the branching contract.

---

## Transcript presentation

On Linux, each command runs beneath a dedicated child subreaper. A completed or cancelled command settles its original leader status and tears down every descendant, including double-forked processes that start another session. Background execution retains the tree while its leader runs; disposal or engine exit tears it down. Descendants are attributed through the supervisor's own parent-child tree, never by comparing launch times between concurrent commands. Stable process descriptors prevent a recycled PID from naming another process. Linux command launches require kernel support for child subreapers and pidfds.

Tool execution and transcript presentation are separate concerns. `tool-presentation.yaml` declares titles, activity headlines and salience, long-running behavior, and which result fields form concise context. A long-running call may appear on the live assistant row while unsettled; ordinary calls appear after settlement. Den reads typed invocation and process fields rather than parsing tool output for “running” or “failed.”

Tool owners retain a readable `display_subject` with each result: the original command, worker brief, delegation task, or scan identity. Durable secret screening covers that subject before the host formats `display_title`. Den prefers the result title over the call's provisional argument title, so completed cards remain readable across pagination, reload, and resource retirement.

Routing handles stay in the expanded arguments. The catalog declares neutral fallbacks for unresolved targets; comparisons name both sides and lists retain their item count. Runtime tools use recognized descriptive fields rather than arbitrary string arguments.

### Activity spans

Every visible tool call projects into an activity span, including a singleton. Consecutive calls share a span only when the same durable workflow run or assistant batch proves membership; the durable transcript still holds the individual calls in order.

Each presentation entry declares an `activity.headline` and named `activity.salience`. Den sums salience by headline; the strongest accumulated headline names the span, with first appearance breaking ties. The compact face keeps running state, failures, and one context hint visible; expanding it reveals the ordered calls through their full renderers.

Native search reports measured file and match counts on its running activity lease. Updates retain the activity and tool-call identities, are throttled to one per second except for phase changes, and cannot reopen a finished lease. Counts describe observed work, not an estimated completion percentage.

### Search in artifact-heavy repositories

`find`, `grep`, `wc`, and `summarize` read their file list from the source catalog and never wait for it to settle. A call waits at most two seconds for a generation newer than the tree, then answers from the last complete one and says so: the result carries `inventory: {"fresh": false, "revision": …}`, and files created or removed since may be missing or present. With no complete generation yet (cold indexing), the call is rejected with `SURVEY_INVENTORY_WARMING`. Warming is not an empty result: nothing in it says the tree holds no matches.

Each call walks the catalog generation top-down and skips a pruned directory whole: one the read profile denies, a nested repository, a hidden directory `grep` was not asked to include, or anything past `max_depth`. A walk therefore costs what it admits, however large the tree behind a pruned directory. A `find` with `name_glob` searches every depth unless `max_depth` is set; a listing without one stops at depth 8 and says so in `depth_notice`.

Discovery `path` values are literal filesystem names; scope options such as `max_depth` and `include_hidden` are separate structured fields, and a missing path uses the tool's ordinary path-not-found code. Discovery globs support brace alternatives such as `**/*.{go,ts}` in addition to `*`, `?`, character classes, and `**`; invalid syntax is rejected with `SURVEY_GLOB_INVALID` before searching. This grammar does not change permission-profile glob semantics.

Ignored directories and generated files remain searchable by default; `grep` and `find` prune them whole when a call passes `include_ignored: false`, except for a walk root named inside an ignored tree. A search that exhausts its deadline reports how many files it searched and which top-level subtree took most of them. `grep` and pattern-mode `summarize` accelerate literal searches of three or more characters by probing the catalog's per-file 3-gram Bloom filters before opening files. The literals a pattern requires include one from each branch of an alternation such as `foo|bar`, so a file the filters show holds none of them stays closed; unsaved editor text, uncached or modified files, and queries without such literals are read directly. `list_dir` and `stat` answer directory listings and child counts from the catalog snapshot without disk walks. `grep` applies its path filter before opening files, rejects binary prefixes before full reads, streams files to a bounded worker pool while applying results in walk order, and bounds concurrent reads across searches; a 60-second execution deadline returns `GREP_DEADLINE_EXCEEDED`, never a successful empty result.

Results count searched text files, binary skips, unreadable files, byte-capped files, and structural parse gaps. Absence applies only to searched text; any unreadable or capped file leaves coverage incomplete. `./task perf:bench` runs the grep engine benchmarks (`grep_engine_bench_test.go`); `PW_GREP_BENCH_ROOT` points it at an existing working tree, read-only.

---

## The open document is the file

A file the person has open in the editor is its editor document, for the agent as for the person. Tool reads of that path serve the document's draft, unsaved edits included; tool writes land in the document and save it. There is one text per open file, and it is the one the person is looking at.

| Tool | With an open document |
|------|-----------------------|
| `read`, `diff` | Serve the draft. The receipt's `source.editor` carries the document revision and `dirty`; an unsaved draft is `recorded: false` with a note, because the ledger records saves, not keystrokes |
| `grep`, `wc` | Search or count the unsaved text for open dirty files; `files_from_editor` and `in_editor` say how many were served that way |
| `write`, `edit`, `replace_lines`, `code_rewrite`, `jq_edit`, `restore_version` | Compute against the draft, land in the document, and save through the person's own journaled save door, recorded as the agent's write. One save publishes every contribution the draft holds, the person's unsaved typing included, and the ledger records each contributor, so such a publication reads as mixed authorship rather than the agent's alone. `EDITOR_DOCUMENT_CHANGING` rejects a document that kept moving under the write. A document diverged from disk, or a publication that fails after acceptance, holds the edit unsaved in the document: the receipt says the file was not written and why, that the disk still holds its earlier bytes for commands and tests, and names the retained state |
| `command`, `verify`, processes | See disk. An agent write saves, so the files it touched are current for them |

A worker on its private branch never sees documents: its tree is a copy. The design and the Den side are in [files-live.md § The open document is the file](files-live.md#the-open-document-is-the-file).

## Source provenance

The source ledger records every project mutation with its actor, and agents read that record through surfaces that share one closed actor vocabulary: `you` / `agent` / `user` / `external`, plus `mixed` for publications with several actor classes and `unknown` for provenance the ledger cannot place (SSOT: `internal/sourceledger` `ActorClass`).

| Surface | What it states |
|---------|----------------|
| Turn-start change brief | Files other actors changed between the previous turn boundary and this one, injected into coordinator assembly; working-set paths itemized, the rest counted (`inject.source_changes`) |
| Read receipts | The ledger identity of the bytes a read served (recorded version id when the tracked head matches, an explicit note when not) plus the file's newest recorded change. `source.editor` states that the person's open document served the text, with its revision and whether it holds unsaved edits |
| Edit rejects | `EDIT_TARGET_CHANGED_BY_OTHERS` when an edit misses **and** the ledger recorded foreign changes to the file during this session, with actor-classified provenance rows |
| `source_history` | On-demand queries: a file's change record (`effects`), per-line authorship over uncommitted state (`lines`), the session's own footprint (`mine`), historical version content (`version`), and version diffs (`diff`) |

`source_history` with `mode: version` returns exact bytes for any retained ledger version in the same line coordinates as `read`; `mode: diff` compares a version with its predecessor, two retained versions, or current head. `restore_version` reverts a file to any retained version, or to its immediate predecessor, as a forward ledger write.

Two invariants hold everywhere. **Absence is unknown, never safe:** a failed lookup, a missing turn boundary, or an untracked path is stated as no record, never as “unchanged” or “yours”. **Facts, not policy:** the host states who changed what and when; what an agent does with that belongs to the project's own agent instructions.

A turn boundary is minted only for a real user-intent message. Host kicks, wakes, and other internal rows continue the current turn; repeating admission for the same user turn returns the original checkpoint, so every turn-relative source query resolves against one immutable lower bound.

Provenance rows are ledger facts, not prose inference ([Host behavior](dispatch-hints.md)). History begins when the ledger first observes a file; git history remains the authority for the committed layer (`git_log` / `git_blame`).

---

## Host process tools

`process_list` discovers or inspects host processes and `process_signal` signals the returned task-bound instance references. Both request approval before their host operation. `process_signal` returns per-target delivery results, so partial failure is visible, and never expands a name, process group, or descendant set after approval. Command jobs keep their own handle controls.

For command behavior native tools cannot express, declare `capability_request.process_control` to keep the sandbox while permitting external signaling, or `capability_request.host_execution` to run the approved command outside it, including passwordless `sudo -n`. Declare the requirement on each invocation, as with `direct_ip`. See [host processes and exceptional execution](security.md#host-processes-and-exceptional-execution).

## Safe-command envelope

The safe-command envelope is the common contract for bounded capabilities that do not need live host coupling: structured argument and project-path validation; filesystem roots and, for spawned MCP servers, process and network confinement; time, byte, item, traversal, and process bounds; stable structured reject codes; and evidence with bounded output projection.

A native Go handler suits a first-party operation. Confined MCP suits an external server whose invocation, transport, and result handling the host can still bound. A bespoke native subsystem owner is for operations that need host database state, journals, project identity, approval-plan construction, or another coupled lifecycle; do not hide those dependencies behind a generic command. Authoring details: [Adding a tool](adding-tools.md).

---

## Structured document edits

`jq` is read-only. `jq_edit` runs a jq program over a JSON, YAML, or TOML document and writes the result in the same format, in place or to `dest`, through the same write door, review, and recording as `write` and `edit`; with `dest`, `path` is only read. The program returns the whole document. A result the file could not faithfully hold is refused rather than written:

| Code | Refused write |
|------|---------------|
| `JQ_EDIT_EMPTY` | The program produced no document |
| `JQ_EDIT_RESULT_COUNT` | A single document became several |
| `JQ_EDIT_STREAM_CAP` | The result stream passed the scan cap, so only part of the file would be written |
| `JQ_EDIT_ENCODE` | A value the format has no representation for, such as `null` in TOML |
| `JQ_EDIT_LOSSY` | Source syntax the rewrite would drop: TOML comments or datetimes, YAML anchors, aliases, and complex keys, or a YAML tag on a changed value |

JSON keeps number literals, key order, indentation, and the trailing newline, and does not escape HTML characters; a stream of one-line documents stays JSON Lines, and a filter may drop records from it. YAML reuses the source node for every unchanged value, so comments, quoting, and literals such as `1.0` survive; a changed value keeps the comments at its position. TOML keeps integer and float types, key order, inline tables, and `[[array]]` tables. New keys follow the source's keys; other formatting is normalized. The encoder's output is well-formed by construction, so the write skips the grammar check below.

## Mutation syntax health

`write`, `edit`, `replace_lines`, and `code_rewrite` evaluate the complete post-edit buffer with the same tree-sitter grammar matrix as the structural tools. The result is `unsupported`, `clean`, `broken`, `incomplete`, or `failed`. Parser timeout, cancellation, failure, and panic are uncertainty and reject the mutation rather than counting as a clean parse.

The mutation boundary is monotonic. A new supported file must be clean, and an edit to clean supported source must remain clean. An already-broken file may be repaired incrementally, but each accepted edit must strictly reduce its recovery-node burden. Overlay promotion is stricter: every changed supported source file in the final merge plan must be fully clean. Reject data identifies the parser fault nearest the changed seam (with visible whitespace and indentation columns for Python); the acceptance rule is grammar-backed and shared across languages.

`replace_lines` has two atomic forms. The direct form replaces one inclusive range. `operations` applies up to 64 non-overlapping ranges against one original snapshot, so coupled changes do not depend on stale intermediate line numbers; a `shift_indent` operation adds or removes an exact whitespace prefix from every nonblank line in its range, and a mismatched dedent changes nothing.

`restore_version` restores a file to a retained source ledger version. Omitting `version_id` restores the immediate predecessor (zero-config undo of the last change); `base_version_id` enforces CAS protection against concurrent edits. Restoring an `absent` predecessor removes a newly created file.

---

## Worker scope coordination

The `task` tool creates a durable child assignment rather than an in-process callback. It validates agent eligibility, project attachment, read/write mode, budgets, and any base-overlay dependency before enqueue.

### Task assignment

| Field | Purpose |
|-------|---------|
| Agent/role | Select an admitted worker capability profile |
| Brief | Give a cold child the problem and relevant context |
| Completion criteria | Define the bounded result expected by the parent |
| Scope mode | Declare read-only or write intent |
| Paths/root | Optionally name the assignment focus |
| Dependency | Name an overlay or upstream result that must exist first |

The brief is bounded because a worker should reacquire source through tools rather than inherit an unreviewable transcript dump. Task paths are guidance only: they do not grant, restrict, or filter reads, searches, mutations, verification, or promotion, and never widen the underlying boundaries.

### Worker workspace provisioning

A read worker uses an isolated session view of the project. A write worker receives a snapshot-backed private tree and host-only metadata; the child never receives the absolute engine path as product context. Subprocess confinement also denies reads from the primary source roots while a write worker is attached to its branch, so an absolute import path, copied editable environment, or tool cache cannot silently execute primary-checkout source.

Provisioning failure settles the job without starting a child. A partially created workspace is reconciled by the worker-workspace subsystem owner, not left for the model to clean up.

### Promotion

Promotion validates current evidence, observed changes, target generation, conflicts, and final syntax health before applying bytes to the integration tree. Syntax is re-evaluated over the prepared target bytes, including command-authored files and caller conflict resolutions. Applying the overlay advances the source generation and records the landed change. A failed preview or promotion preserves the overlay for repair; rejection closes it intentionally. See [Coordination](coordination.md#worker-workspace-and-promotion).

### Caps

Worker count, loop count, tool bytes, assignment size, path count, and overlay size are host budgets whose exact values live in catalogs and code. Overlay capture enforces explicit ceilings (10,000 changed files, 100 MiB total modified bytes). When an overlay exceeds these budgets, the worker branch is left unsealed on disk for review rather than discarding changes or retrying unbounded captures. The invariant is that every dimension is bounded and truncation is stated. Budget exhaustion produces a structured partial or blocked result and leaves a resumable child when repair is possible; it never converts incomplete work into success.

---

## Pack board

`pack_board` is a bounded orientation projection assembled from current host state: project roots, repository summary, workflow, workers, progress, overlays, scans, and other active facts. It is not a writable coordination document or a worker-status polling loop; it helps a coordinator decide what to inspect next, and the underlying domain facts remain authoritative. Small and empty repositories are represented explicitly: a warming source catalog is not reported as empty.

### Scan visibility (host inject)

When scan state is relevant, the host projects summary and freshness into the board and workflow context. Detailed findings remain behind scan tools so the prompt does not grow with repository size.

---

## Command composition

Command grammar guidance is defined in the catalog's command schema and worker partial; the executor appends only profile-specific native-tool alternatives to the description.

`command` accepts structured argv stages. The host tokenizes a small declared composition grammar for sequential conditions and pipes where supported; it does not reopen a general shell interpreter. Parsing never changes the call's arguments: one plan joins the stages with the explicit `stdin`, `stdin_from`, `stdout_to`, `stderr_to`, and `append` fields, and approval, detection packs, and the executor all read that plan.

### Redirection

Each stage keeps its own redirections, applied left to right as a shell opens them: `>`, `>>`, `2>`, `2>>`, `&>`, `&>>`, `<`, `2>&1`, and `1>&2`. A quoted target is one path, and `/dev/null` discards output or supplies empty input. `cmd 2>&1 > out` sends stderr where stdout pointed before `> out`; `cmd > out 2>&1` sends both to `out`. Stdout and stderr keep separate append modes.

Paths resolve under the call's `cwd` and cross the same write scope as `write`; the approval card lists every redirected file. A group's output lands when the group exits, so `make > build.log && grep error build.log` reads what the first program wrote.

The host refuses, with `COMMAND_REDIRECTION_UNSUPPORTED`, the forms it cannot run as written: descriptors other than 0, 1, and 2; `>&-`, `<>`, and `<&`; a stage before `|` that sends no output into the pipe; a stage after `|` that also reads a file; and an inline redirection alongside the structured field for the same stream. Here-documents remain `COMMAND_NOT_ARGV`; pass the body as `stdin`. Terminal surfaces own every stream and refuse file redirections.

### Globs

Unquoted `*`, `?`, and `[...]` expand once, before secret references resolve and before any card, so every card and the executed process read the same argv. The grammar is declared and bounded rather than a shell's:

- quoted or backslash-escaped pattern characters are literal, so `find . -name '*.go'` passes the pattern through;
- a component matches a leading `.` only when it starts with `.`;
- `**` as a whole component matches any depth of non-hidden directories without following symlinks;
- a pattern with no match stays literal, and an argument starting with `-` never expands;
- matches are sorted and resolve under the stage's `cwd`, in the worker's private branch whenever the call runs there;
- a path the call's boundary cannot read before any grant, including the control plane, is not a match; a later grant never widens what a pattern named.

One pattern may match at most 1,000 paths and one plan may examine at most 50,000 directory entries. Exceeding either returns `COMMAND_GLOB_BUDGET_EXCEEDED` naming the pattern; nothing is truncated. Redirection targets and environment values never expand.

Working directory and environment are per-call inputs; there is no mutable session shell state whose hidden `cd` or export changes later commands. Structured I/O and background handles replace shell idioms that would obscure resource lifetime: a stage that becomes long-running returns a handle, and output and stop use explicit control tools. Stopping a handle, cancelling the call that awaits it, or stopping the session ends the whole process session the command started, including jobs a shell moved into their own process groups: SIGTERM first, then SIGKILL for anything still running two seconds later.

A settled foreground result carries the last 8 KiB of screened output as `tail`. When the process wrote more, the result says so (`truncated`, `original_tail_bytes`) and names `wire_spill_path`, a host-data file holding every retained byte, readable by line with `read` `offset`/`limit`. A redirect into the tree is never needed to see the rest.

A confined result also lists `sandbox_refusals`: each operation the kernel refused the invocation's processes, with its target, process, count, and the capability that would admit it ([Denials are legible](security.md#denials-are-legible)). A running result and `command_output` list the refusals so far; a finished one lists the settled record.

A run can fail for a reason its exit status does not carry. The foreground result, `command_output` for a job, and the job's completion then state `exec_failure` with a `kind` (`redirect_commit` when redirected output did not land, `stage_launch` when a later stage never started, `run` otherwise) and the error as `detail`. Such a run is never `ok`, and a verification check that ends this way fails. A timeout is reported by `termination_reason`, not here.

### Command job lifecycle

A bounded foreground command may settle with captured output. A long-running command becomes a durable job reference with process-local control: spawn under the compiled confinement profile; record the handle and `owner_ref`; expose bounded output reads and an explicit stop action; settle on exit, stop, session teardown, or interruption. The host stops only processes it launched; process discovery by command-line or binary name is never authority.

`command_stop` reports `stop_requested` separately from `running`. Request stop once, then wait or read the handle until it settles; a kill request is not proof of exit.

### Wait

`wait` parks the calling agent without occupying a worker slot. A wait has a bounded timer backstop (60 seconds by default) and may subscribe to role-appropriate host events, exact process handles, an HTTP status range, or an exact loopback port. The published schema is pruned to the calling profile, so worker-only roles never receive coordinator-only event kinds.

For a known command, `wait(until_complete=true, conditions=[{"kind":"process_done","handles":["<handle>"]}])` requires exact handles owned by the calling session and wakes on the first selected completion, loss of process ownership, or kernel refusal no earlier result showed. A `next_worker_done` condition may likewise name task ids in `handles`; other workers finishing do not wake it. A refusal settles the condition with `outcome: refused` while the job keeps running. Every process wake carries the host's report of the job: how it ended, or what was refused, with the recovery guidance that applies. Omit `timeout_ms` to wait without a deadline, or pass it as an explicit backstop. The host reconciles process state without periodic model turns, including exits before subscription and unavailable handles after restart. `resume=true`, restart recovery, and host re-arms keep both the conditions and the deadline mode unless explicitly replaced. `scan_done` concerns scans the session requested; automatic scans of the same tree neither hold it open nor wake it. A denied HTTP readiness destination ends the wait with `HTTP_REQUEST_HOST_DENIED`; it is not disguised as a later timeout.

A wait lease is durable: it persists the addressed session, optional worker job, active profile, optional deadline, conditions, and exact loopback ports, and restart reconstructs subscriptions and probes. The first condition or deadline settles it. Worker waits return through the durable worker queue, coordinator waits keep coordinator workflow gating, and direct agent modes re-enter the same session. Delivery uses the lease id as an idempotent receipt; live delivery retries with bounded backoff, and restart recovery can replay a settled wake without duplicating an admitted one.

Coordinator wakes retain their event identity, target, batch, and workflow revision while queued. One consumer drains a session's queue; each successful model request acknowledges the facts captured for it, and failed requests do not consume wakes. A matching event settles the lease and delivers its result even when optional workflow gates would suppress an ambient turn; a fresh permitted workflow interruption ends the lease before model execution starts; unrelated process events leave the selected handle's wait armed.

### Verify verdicts

`verify` and eligible command subsystem owners state a structured terminal source verdict bound to the workspace generation they checked. Consumers read that receipt fact; they do not search stdout for “PASS.”

A running, failed, stopped, timed-out, stale, or boundary-refused check cannot pass an explicit workflow test gate. `unverifiable` records a boundary refusal; other failures in the same run remain diagnostic evidence. Routine completion and worker promotion use validation as advisory information, not an admission requirement.

A selected project command is a default. Bare `verify()` runs it, while `command(verification: true)` nominates a targeted check. An identical selected command counts through either tool, with the same capability request and approval semantics. `declared_command_match` records identity, never success. Foreground and background checks carry launch-time source identity and one terminal check id, so duplicate receipts cannot spend the recovery budget twice.

---

## Capability request

Some command and terminal actions need authority that cannot be inferred from argv alone: an external write root, protected read, local listener, loopback connection, direct IP route, mediated destination, named host resource, process control, or host execution. Terminals take neither direct IP nor the execution capabilities, because a held session outlives the invocation they would bind to. The model declares that need in structured `capability_request` fields before spawn. The host validates whether the current surface can provide it, builds an approval plan where appropriate, and compiles the resulting authority into the execution boundary.

Declaring a capability is not receiving it. Unsupported, unavailable, or denied requests fail before the effect starts. A reusable grant matches typed facts and current confinement, not similar prose or program names.

### Mediated SOCKS environment (`socks_proxy`)

`socks_proxy` on `command` provides proxy environment and broker routing for a program that can honor it. The broker observes destinations before dial and may hold a connection for approval. Software that ignores the proxy receives no fabricated destination evidence; if direct network authority is separately requested, the approval and reporting say that the destination may be unobserved.

---

## Interactive exec (PTY)

The host composes command launching, terminal interaction, captured output, and process lifetime as separate services over one synchronized process table. Terminal operations use the terminal service; output screening and publication use the output service; awaiting, stopping, index-watch transfer, and session teardown use the lifecycle service. Shared admission and process state keep launch, shutdown, and completion atomic across these services.

Terminal execution has two intentional lifetimes:

- `command` with `terminal_capture` runs one exact process in a sealed PTY, returns its settled virtual screen, and creates no mutable terminal handle. The capability request and the captured screen therefore describe the same reviewed action; this is the terminal-proof path for non-interactive programs and direct-network actions. Keep captures to one argv line and read `exit_code` from the result; for sequences or pipelines, omit `terminal_capture` and capture the final program separately.
- Interactive terminal tools manage one held PTY as a session resource when the caller must send input or observe multiple states: open with frozen argv, cwd, environment, confinement, and initial capability; send bounded input; read bounded stream output; capture a final screen or semantic snapshot; close explicitly or at session teardown.

Authority cannot be widened after the process starts. A held terminal cannot carry one-action direct-network authority across turns; use a sealed `command` capture when the same process needs both that authority and terminal evidence. A later network or listener need requires a new process opened with that compiled boundary.

PTY input is sensitive by default and is not echoed into observer projections as ordinary output. Sealed captures and held-terminal snapshots produce terminal surface evidence, distinguishing grid state, raw log, and optional visual artifact, so each claim cites the correct evidence shape; flat command output remains command evidence. Held processes inherit the same project, grant, signal, and egress constraints as other command effects.

---

## HTTP actions

`http_request` sends one bounded API, webhook, or development-service request with an explicit method, ordered headers, one body source, redirect policy, and structured response facts. It shares the pinned-hop transport, egress approval, loopback capability, and outbound-secret boundary with other first-party HTTP operations, and inherits no research extraction or caching. `wait` handles repeated readiness checks so an agent does not rebuild polling with request loops.

The request side covers what an agent reaches for a shell client to do: `query` parameters (appended in order; the URL's own query string is never reparsed, so a signed query survives), an `auth` object (basic credentials encoded by the host after secret substitution, or a bearer token), one body source among `body_text`, `body_json`, `body_form`, `body_path`, and multipart `form` parts (in-memory bodies up to 32 MiB), a named `cookie_jar`, and an optional `unix_socket` for local daemons.

A jar is a chat-scoped managed secret with origin `cookie_jar` ([Secrets](secrets.md#origins-and-scopes)): the host sends the stored cookies that match each hop's URL under RFC 6265 domain, path, and secure rules (`internal/httpcookies`), stores what the response sets, and reports cookie names, counts, and the jar reference. Values are screened like any managed value and never appear in results. A `{{cookie:name}}` reference in a header value or body is placed by the host after the outbound screen, which is how a double-submit CSRF token goes back without the model holding it; an unheld name is refused with `HTTP_REQUEST_COOKIE_NOT_HELD`. A jar is written back whether or not the exchange succeeded, and a jar that cannot be persisted is stated on the receipt as `persisted: false` rather than raised, because the request is already on the wire.

A `token_jar` holds what `capture_tokens` extracted from response headers or a JSON body, each token bound to the destination whose response carried it; the receipt lists names and issuers, never values. A `{{token:name}}` reference is placed by the host after the outbound screen when the request goes back to the token's issuer. A request to any other destination is a disclosure the outbound secret screen decides first, with the same card, standing decisions, denial code, and redacted-send receipt as a resolved secret reference ([Secrets](secrets.md#origins-and-scopes)); an unheld name is refused with `HTTP_REQUEST_TOKEN_NOT_HELD`.

Results carry `sent_headers` beside received status and headers: the headers as the call stated them, with managed secret and cookie references left as reference tokens, an `auth` header shown by scheme alone, and host-added headers such as `Content-Type` as sent. `final_url` and each `redirects` hop report the chain actually followed, except that a substituted secret value, plain or percent-encoded, reads as its reference token. No credential the host substituted or encoded reaches the result.

`unix_socket` names the socket the request travels through, absolute or relative to the project. The socket is the destination: the executor reviews it as exact socket authority before the handler runs (the same subject `capability_request.socket_paths` gives a command), the URL supplies only the request path and `Host` header, a redirect to another origin is refused, and the result, the retrieval label, and the outbound-secret recipient all name the socket ([local sockets and daemon authority](security.md#local-sockets-and-daemon-authority)).

The response body has four dispositions: inline when textual and under 128 KiB; landed under host data at `body_spill_path` when textual and larger (the same tool-output store an oversized result spills to, readable with `read` or `jq`); discarded with `response_body: discard`; or landed in the project with `response_path`, streaming through the same write door and jail as a fetched asset (`internal/tools/inboundwrite`) up to 256 MiB within `timeout_ms`, with a receipt (path, bytes, digest) instead of the bytes. A HEAD response reports no size or digest.

Every disposition sees the response only after the host has scrubbed it: a held token reads as its `{{token:name}}` reference and a value the call resolved reads as its secret reference, in plain and serialized spellings, before a byte is placed inline, landed in the project, or spilled under host data. A body that could echo either is buffered rather than streamed so the scrub can run first. When a scrub changed anything the result says `body_redacted: true`, because the placed bytes then differ from the received bytes that `bytes` and `sha256` describe.

Redirects under `redirects: safe` follow RFC 9110 method rewriting (303 becomes GET, 301 and 302 rewrite POST to GET, 307 and 308 preserve method and body), so a POST that ends in a see-other lands on the resource rather than handing the model a bare 3xx. Caller headers drop when the origin changes, and a cross-origin hop that would re-send the body fails: those bytes were screened against the declared destination. Each hop records the URL that answered, its status, and where it pointed.

Failures separate what a retry can fix from what it cannot. A bounded body, a refused redirect chain, and an unusable declaration are permanent (`internal/outboundhttp` typed faults) and come back with `retryable: false`; a dial, a reset, or a deadline stays retryable and names `timeout_ms` when the deadline was the cause. A transfer that stops mid-download is a transfer failure, not a failure of its sink.

Requesting `command` does not automatically load `http_request`; the coordinator can request that native tool when needed. An exact `curl` shape may be parsed before execution; agent-visible recovery is `USE_HTTP_REQUEST_NATIVE` with validated native entries in `replacement_calls`, never parser examples. The parsed grammar covers one request or a `;` / `&&` sequence with `echo` separators and carries the runner envelope: `cwd` joins onto body, response, and socket paths; `loopback_connect` is carried; a loopback URL declares its own port unless `--unix-socket` makes the socket the destination; `timeout_ms` is carried only when exactly one produced call owns the deadline and the native schema can hold it. Cookie files become a jar named after the file, basic credentials become `auth`, form parts become `form`, and url-encoded data is encoded into the body. Insecure TLS, a `||` gate, an unsupported capability request, and any `--data-urlencode` or `-F` operand that reads a file into an encoded body have no native shape and stay on the command boundary.

## Web research sources

`web_search` and `fetch_url` are document-research tools whose availability depends on the session's web-research setting and effective provider catalog. `fetch_url` retrieves a source for extraction or a bounded raw asset; it is not a general HTTP client. Text mode pages and maps a cached body by line (a minified JSON document is re-indented before caching); raw mode returns the bytes verbatim.

Direct search uses a catalogued set of keyless sources with declared roles, pacing, and normalization; user-configured providers join the effective set according to settings. Provider ids, endpoints, and enablement live in the generated catalog (`lycaon/config/packs/painted-wolf/web-research/host/web-research-providers.yaml`), not this page.

Search results are untrusted inbound content. Provider text is normalized at one fan-in, provenance is retained, and fetched bodies are marked as data before entering model context. Outbound destination and secret policy still apply. Caching and pacing are explicit and bounded: a cached result states that it is a repeat, and provider cooldown does not turn one failure into a permanent absence. Privacy behavior and the outbound inventory are in [Privacy](privacy.md).
