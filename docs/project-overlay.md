# Project overlay

The project overlay is commit-worthy configuration under `.paintedwolf/`. It lets a repository carry guidance, workflows, policy, and other declared artifacts without turning repository content into host authority.

Every build channel reads and writes the same `.paintedwolf/` directory. Host configuration, credentials, and session state remain separate under the channel-specific application directories.

**See also:** [Projects](projects.md) · [Extend](extend.md) · [Security](security.md#trust-boundaries) · [Compatibility](compatibility.md) · [AGENTS.md standard](agents-md-standard.md)

---

## Why the overlay is project content

Everything under `{root}/.paintedwolf/` is meant to be readable, editable, reviewable, and optionally committed with the project. It contains configuration, not hidden engine state. Worker write trees, session rewind checkpoints, tool-result and attachment bodies, indexes, caches, downloaded data, and process runtime state all stay outside the roots.

Two consequences follow: session rewind may restore overlay files changed by a turn, like any other project file; and granting and credential material must never live in the overlay, because a repository must not authorize itself.

### Agents change the overlay only under review

"Readable, editable, reviewable" describes a person. Every overlay file a trust surface loads (settings files, postures, rules, workflows, scan configuration, skills, prompt overrides, and the overlay AGENTS.md) is **agent policy**: an agent may read it and propose a change, and the change reaches the `agent_policy_change` gate with its exact diff through every write door: file writes, content application, archive extraction, native Git operations, and an exact command grant. The sandbox keeps these files unwritable for commands that did not declare them. Files no trust surface loads, such as blueprints and scratch, are ordinary.

This is what makes "repository policy may tighten but never grant" hold in practice. An agent that could edit `approvals.yaml` unreviewed could relax the project's own rules, and one that could edit `extensions.lock.yaml` could change what code loads next turn. Review of the exact bytes is the enforcement; the merge algebra below is only the reading half. The host's own state tree is the control plane, and path-scoped approvals cannot open it. Explicitly approved [host execution](security.md#host-processes-and-exceptional-execution) removes the command sandbox, including this restriction.

Human edits in an editor or a review flow are ordinary; the host expects out-of-band edits and rebuilds against them.

## Merge order

For primary root **P** and active root **R**, effective project content resolves in this order:

```text
1. Bundled and admitted device content
2. Primary-root .paintedwolf/ project-wide content
3. Active-root .paintedwolf/ root-specific content
4. Active-root AGENTS.md ancestor chain
```

No-folder projects (zero roots, reached by detaching every root) use bundled and device content only. A draft project is not a no-folder project: it has one root, the host-created draft scratch root (see [Projects](projects.md#draft-project)), and resolves through the ordinary primary-root path. That scratch directory starts empty, so a fresh draft effectively sees bundled and device content only.

Later layers do not all mean "replace earlier". Each artifact kind declares its own merge algebra, and the loader applies that declaration.

## Merge algebras

The set of artifact families is not fixed, and enumerating it here would be a second inventory that is wrong the day a family is added. The registered settings basenames are `settingsoverlay.SettingsOverlayBasenames()`; the directory-shaped families (rules, workflows, prompt overrides, skills) and the remaining project-only files are discovered by `internal/projectcontrib`. Those two are the inventory.

What endures is the small set of algebras a family may declare:

| Algebra | Used when | Consequence |
|---------|-----------|-------------|
| **Additive tightening** | The artifact expresses policy: approval rules, OAR rules, detection-pack enablement | Deny wins, and a project can only narrow. A repository cannot grant, because a clone would then arrive carrying its own permissions |
| **Keyed overlay** | The artifact assigns a value per stable key: model policy, session postures, scan hints | Bundled, device, primary-root, then active-root layers replace per key. Unaddressed keys keep their earlier value, so a partial project file is not a wholesale redefinition |
| **Union across roots** | The artifact records a project-local fact with no natural winner: scan ignores | Every root's entries apply after the bundled ones. There is no device layer: a device-wide ignore would hide a finding from unrelated projects |
| **Enable-only selection** | The artifact selects from something the device already installed: extensions, MCP providers, detection packs | The project may turn a unit off or on within what the device admitted; it can neither install, define, nor reach further |
| **Explicit collision resolution** | Two enabled packs contribute the same unit id: workflows, prompt bindings, and every other contributed unit | The unit loads nothing until a person names a winner. Load order is not an answer a person could have meant |
| **Ordinary project files** | Content a person reads, not configuration the loader resolves: blueprints, `AGENTS.md` | No merge at all. `AGENTS.md` follows filesystem ancestor semantics instead |

### Blueprints

Blueprints are Markdown documents stored under `.paintedwolf/blueprints/`. Each blueprint file carries YAML frontmatter with a stable UUID `id` and lifecycle metadata such as `status` (`draft`, `approved`). The host writes `id` when it creates a blueprint; a hand-authored file without one is identified by a UUID derived from its project and path, which the host's next write of the file records in its frontmatter. Reads never write. Blueprints are addressed by that `id` under `/v1/projects/{id}/blueprints/{blueprint_id}`; `GET /v1/projects/{id}/blueprints?path=` finds the blueprint at a path.

### Standing patterns

`standing-patterns.yaml` is the one overlay family that produces *observations* rather than policy. A project declares named anti-patterns it does not want reintroduced (a structural pattern, optionally narrowed by language and path scope), and the host counts matches across the tree and surfaces the counts on the board.

It lives in the overlay because the patterns are repository knowledge: what counts as a regression in one codebase is idiomatic in another, and the declaration is worth reviewing alongside the code it governs. It carries no authority. A match raises no ask, blocks no tool, and changes no permission; a rule that fails validation is skipped and reported rather than silently ignored.

---

## Project trust

Project trust shows content a project supplies to the agent and provides off switches. It is not an effect permission system. Instructions, skills, settings, MCP configuration, scan configuration, prompt overrides, and extension settings apply automatically when both their device switch and project switch are on:

```text
applies(surface, project)
  = device_enabled(surface) AND project_enabled(surface, project)
```

The host keeps the effect boundary independent:

- project approval rules and posture may tighten device policy but cannot grant, save, or widen authority;
- project MCP may select only device-configured loopback providers and cannot add remote endpoints or credentials;
- project workflows and rules use the host's closed vocabularies;
- confinement, egress, protected paths, and effect approvals continue to govern every action.

The Trust UI groups surfaces by what they do (steer the agent, replace an app default, or suggest an install). The grouping is explanatory, not an authority level. Unknown surface ids are rejected rather than given a fallback meaning.

Read baselines drive only the Trust change marker. Adding, editing, or removing a file changes the marker, including removal of the last file or detachment of a root. Reading configuration never pauses or authorizes a surface.

The sidebar chip opens a compact summary of the same host-owned comparison shown in the project's Trust group tab. Counts and category rows come from that comparison's changed files, not from configured inventory. Every category link opens the whole group; files shared by several categories appear once, and root identities keep equal paths in separate folders distinct. Opening Trust clears the unread indicator without changing the comparison, and background refreshes publish later changes without marking them read. The host retains the latest comparison across restarts, including removed files and detached roots. Trust uses the same grouped file list as Walk Git reviews; each row opens its immutable before/after diff in a regular file tab.

Baseline commits compare the captured project roots and previous read state. A concurrent opening cannot overwrite a newer baseline, and a failed or incomplete capture leaves the baseline intact. Retained configuration text is bounded to 4 MB per UTF-8 file and 32 MB or 4096 files per snapshot; unsupported content reports an error rather than silently clearing its marker. Baseline text lives separately from lightweight project rows and survives restart and backups.

Trust discovery shares a bounded directory inventory across requests. Watcher events invalidate the affected paths; incomplete or nonrecursive coverage also requires periodic revalidation on access. Concurrent cold requests share one walk, and discovery runs with bounded concurrency and a deadline. The inventory retains paths only: each response reads current contribution bytes and joins the current switches, and last-read stamps use that same response snapshot.

Extension settings and extension suggestions are separate Trust surfaces. The first controls project-disabled extension units. Suggestions are inert until a person selects them; acceptance is bound to the exact parsed proposal and device catalog revisions, validates every selected id before mutation, and installs at device scope. The install dialog appears only when there is an actual choice.

Device and project switches are independent off controls. Runtime content changes on the next turn; suggestion visibility and acceptance change immediately. A turn already in flight keeps the immutable catalog frame it compiled at admission.

---

## Scan overlays

Scan configuration separates device integration from repository-specific meaning.

### `scan-hints.yaml`

Hints map scanner rule ids or namespaces to agent-public guidance codes. They layer host stock mappings, then device mappings for installed scanners, then project mappings for repository-specific interpretation. The device layer exists because scanner installation belongs to the machine; the project layer refines meaning without redefining the scanner.

Matching prefers an exact rule id, then the longest declared namespace prefix, then the scan kind, then the default. This is declared identifier matching, not prose classification.

### `ignores.yaml`

One project file contains two independent sections: `findings` records scanner decisions and `secrets` declares exact public values that are not credentials. Security and Secrets keep their separate interfaces; both read and edit this document. Writes preserve the other section and YAML comments and reject concurrent edits instead of overwriting them. Agent writes use the same policy-write approval boundary as the other agent-policy files; reads need no approval.

Both sections apply when the project's **Scanning and ignores** trust surface is enabled, including declarations arriving through version control. The host re-reads changed declarations without requiring another scan. There is no per-entry acceptance step: edit the file to change an exception and remove its entry to stop applying it. Persistent classifier exceptions are not stored in Saved approvals.

A `findings` entry is a conjunction of the predicates it names, and a finding is ignored when it matches all of them:

```yaml
version: 1
findings:
  - id: 6f1c…                    # host-assigned, stable, so a client can withdraw this entry
    path: test/**                # repo-relative glob; a bare directory covers what is under it
    kind: sast                   # host finding classification
    scanner: opengrep-sast       # empty spans every engine, which is the point of the file
    rule: opengrep:go.lang.xss   # engine vocabulary, matched as a glob
    advisory: CVE-2026-21102     # matches every alias the host resolved for one vulnerability
    fingerprint: abc123          # exactly one finding
    reason: fixture material     # required
    justification: vulnerable_code_not_in_execute_path  # OpenVEX; advisory entries only
    expires: 2026-12-09          # calendar day, exclusive
```

Every predicate is optional; an entry naming none is refused rather than silently ignoring everything. Wherever the host owns the fact (path, kind, canonical advisory) one entry covers every engine that reports the same thing. A rule id is one engine's vocabulary, so an entry naming one names its scanner too. The engines still honour their own ignore files first; this is the layer above them.

An ignored finding is still scanned, recorded, and counted. It leaves the open list and stops reaching the agent, which is what makes withdrawing an entry take effect without a rescan. A lapsed entry stays in the file, visible and renewable, and stops applying; its findings return to the open list on their own. A mistyped entry disables itself and is reported as not in force; it never disables the file or the scan.

The `secrets` section classifies exact UTF-8 values, with no path, regex, prefix, or scanner predicates:

```yaml
secrets:
  - id: public-example-token
    value: "an-exact-public-fixture-value"
    reason: Published example, never used as a credential
    expires: 2027-01-01
```

Values are plaintext and must be safe to commit. Whitespace is significant. A reason is required; expiry is exclusive midnight UTC. IDs are optional labels. Invalid entries are reported individually and do not disable valid peers or the other section.

Active entries apply to project-attributed secret screening and to scanner findings for which the host has an exact value identity. Protected credentials always take precedence. Scanner finding ignores do not disable runtime secret screening, and a secret exception does not create a second `findings` entry. See [Secrets and redaction](secrets.md#ignored-public-values).

### `source-scope.yaml`

A project may say what the host's own observation of its tree admits ([Scan findings § Source scope](scan-findings.md#source-scope)). The file applies only when the project's scan-config surface is trusted, because it changes what security scans see:

```yaml
version: 1
exclude: ["fixtures/huge/"]      # gitignore grammar, relative to the root; left out of capture
include: ["generated/"]          # admitted past an ignore file that would leave it out
budgets:
  capture: { walk_entries: 5000000 }
  catalog: { subtree_entries: 500000 }
```

`exclude` narrows and `include` widens past the tree's ignore files; neither reaches the excludes floor or engine metadata, so the floor stays a floor. `budgets` replace the device's for this root in either direction: raising one is the project's own cost, and the record of what a budget left unobserved travels with every generation regardless. The catalog behind Files, search, quick-open, and summaries honours only the budgets; it leaves nothing out because of a declaration or an ignore file.

Under the algebras above this is a keyed overlay for budgets and, for the pattern lists, a project-only statement with no device layer.

### Detection packs — project enable-only

A project may enable a detection pack already installed on the device. It cannot disable one, define detection rules, or install a pack through project content.

Enablement is the only direction because it is the additive one: it can raise more asks and silence nothing. A clone arrives with its overlay already in the tree and the governing trust surface defaults to on, so a row that turned a pack off would let unread repository content decide which rules a person never sees fire. Turning a pack off stays a device decision made in Settings. A row asking for one is refused and shown as ignored, alongside rows naming an uninstalled pack or a field other than `id` and `enabled`.

Detection logic changes which execution events raise an additional ask, so rule bodies are device-scoped. The repository may state that an admitted pack is relevant to its work; it cannot author the matcher that judges its own commands. Project detection enablement applies to project-associated tool execution; mediated egress without project identity remains device policy.

---

## Resolver and warm paths

The resolver receives the ordered root identities and produces one effective content view per artifact family. Cache identity includes the root generation, active root, admitted extension frame, trust switches, source revisions, and relevant device configuration. Callers consume the resolved view rather than walking overlay directories themselves, so prompt assembly, tool policy, Den settings, and validation cannot apply different precedence to the same files. A cache validates against current stamps and generations or rebuilds; it never treats a prior read as durable authority.

Project open may warm catalogs and validation results in the background. Until a complete generation exists, callers report warming or unknown rather than an authoritative empty overlay. Turn compilation captures one immutable contribution frame; mid-turn filesystem changes affect the next frame, not half of the current request.

## `AGENTS.md` coordination

`AGENTS.md` follows filesystem ancestor semantics rather than overlay unit resolution: the active file's directory determines the applicable chain from root to leaf. The host exposes the resolved guidance and its source boundaries to the model without interpreting the prose as permission, workflow state, or a host gate. Human-only write paths remain enforced by the project tool boundary. Details: [AGENTS.md standard](agents-md-standard.md).

## Compatibility

Project overlay files are `user-repo` surfaces. Additive keys are preferred, and a breaking interpretation requires the overlay format version to advance.

The primary root carries the format marker, `overlay_format` in `.paintedwolf/overlay.yaml`; a missing file reads as format 1. The host validates it before reading any project-controlled artifact, including settings and suggestions not reached through a session, and fails closed on a format newer than it supports rather than partially applying policy it does not understand.

`ignores.yaml` requires `overlay_format: 3` in the same root's `overlay.yaml`. Host writes publish the marker first, preserving its other keys and comments. Overlays without ignore artifacts remain accepted at their supported format. The retired `scan-suppressions.yaml` and `scan-ignores.yaml` files, missing markers on existing ignore documents, and unknown formats are refused without modification; pre-v1 has no compatibility reader or automatic conversion. `ignores.yaml` itself requires integer `version: 1` and only the `findings` and `secrets` sections.

## Invariants

- `.paintedwolf/` contains project content, never hidden engine state or credentials.
- Repository policy may tighten but never grant or lower the host floor.
- Every project trust surface uses device enablement and project enablement.
- Last-read stamps report changes and never grant authority.
- Extension suggestions remain inert until selected against current revisions.
- Each artifact family declares its merge algebra, and one resolver supplies the effective view to every consumer.
- Incomplete warming is represented explicitly, never as an authoritative empty result.
