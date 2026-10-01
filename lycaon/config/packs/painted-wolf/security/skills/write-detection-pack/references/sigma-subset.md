# Detection pack Sigma subset

Maintained source of truth: `docs/security.md`. Detection packs are an additive overlay on containment and approval posture — never a replacement floor. A rule miss is not evidence of safety.

## Pack shape

```
<pack_id>/
  pack.yaml
  rules/<slug>.yml
  fixtures.yaml
```

```yaml
# pack.yaml
id: example-cli
label: Example CLI
description: High-risk external actions observed on the agent execution plane.
```

```yaml
# fixtures.yaml
fixtures:
  - rule: destroy
    positive:
      - "tool destroy --force"
    negative:
      - "tool plan"
```

Each rule needs a fresh RFC 4122 UUID `id`, and at least one positive and one negative fixture. Caps: max 200 rule files, 64 KiB per file, 4 MiB per pack.

## Accepted top-level keys

`title`, `id`, `description`, `logsource`, `detection`, `level`, and optional `status`, `references`, `author`, `date`, `tags`, `falsepositives`. Any other top-level key marks the rule unsupported (inactive with a reason).

Required: UUID `id`, non-empty `title`, `level`, and `detection` with ≥1 selection plus `condition`.

## Effect tags and level

`tags` is where a rule declares what its match means. It is optional to the parser
and required in practice: a rule with no effect tag fails toward asking, and one
with the wrong pair fails a contract test.

| Tag | Declare it when the effect lands on |
|-----|-------------------------------------|
| `lycaon.effect.external` | An account or service beyond this machine |
| `lycaon.effect.local` | This machine itself |
| `lycaon.effect.unrecoverable` | Nothing can put it back |

Pick **exactly one** of `external` / `local` — the card states one place, so declaring
both is rejected. Then pair `level` with `unrecoverable`:

- `critical` — cannot be undone, or changes who has access. Must declare `unrecoverable`.
- `high` — expensive or hard to undo, access untouched. Must declare `unrecoverable`.
- `medium` — a real but reversible effect, or a **coarse** signal (matched by hostname
  or by a verb alone). Must **not** declare `unrecoverable`.

A coarse match sits one band below the precise rule for the same operation: a hostname
cannot tell a read from a charge, and a verb alone cannot tell prod from scratch.

```yaml
tags:
  - lycaon.effect.local
  - lycaon.effect.unrecoverable
level: high
```

## Logsource shapes (exactly two)

- `{product: lycaon, service: tool_exec}`
- `{product: lycaon, service: egress_observed}`

### Fields

`tool_exec` — action identity and shape:

`Tool`, `Image`, `CommandLine`, `Argv`, `ApiAction`, `ProjectDir`, `SessionId`, `ActionId`

Argument content — populated for **every** tool, including MCP tools and native tools that carry no argv:

`ToolArg`, `TargetFile`

`ToolArg` holds one `name=value` entry per short scalar argument, nested keys dotted (`nested.url=https://…`), so a rule reads `ToolArg|contains: 'query=169.254.169.254'`. Values over 256 bytes are dropped, not truncated — file bodies never reach a rule. `TargetFile` holds the paths the action names. Both are capped at 32 entries. For `command`-family tools these sit alongside `CommandLine`, which remains the real argv.

Containment and capability context:

`Contained`, `EgressMode`, `FSJailed`, `RootsDigest`, `LoopbackAccess`, `DirectIp`, `Visibility`, `DeclaredDestination`, `SocketCount`, `SocketCapability`, `SocketApprovedPath`, `SocketResolvedPath`, `SocketScope`, `SocketGrantState`, `EffectiveAuthority`

Host-derived destination class (always populated; default `unproven`):

`EffectReach` — `local` | `remote` | `unproven`. Bundled `tool_exec` rules include `filter_local: { EffectReach: local }` and `not filter_local` so LocalStack, loopback registries, and similar proven-local destinations stay quiet. Do not omit that filter on new `tool_exec` rules. Local destructive work still asks because the host leaves those actions `unproven`.

Consequence enums — closed vocabularies, and **currently unpopulated by the host**, so a rule keying on them loads but never fires; do not use them yet:

`ActionEffect`, `TargetScope`, `PrincipalScope`, `CredentialPersistence`, `BulkAction`, `AmountPresent`, `TargetPresent`

`egress_observed`:

`DestinationHostname`, `DestinationPort`, `DestinationIp`, `Transport`, `Image`, `Initiated`, `SessionId`, `ActionId`, `Origin`, `DecisionStage`

`CommandLine`, `Argv`, and `DeclaredDestination` hold free-form text the model wrote. Match them freely — that is what a Sigma rule is for, and a command line authored by the thing being watched is the canonical SIEM log source. A rule a determined model could word its way around is still worth shipping: escalation is additive and a miss falls through to the floor. What stays out of a rule is conversation and transcript text, which are not events at all.

Do not invent fields. Do not embed cloud-action allow/deny lists in prose or Go — severity comes from rule `level` and posture, not hand-maintained action catalogs in this skill.

## Rehearsal filters

Match a flag as an argv token, not as command-line text:

```yaml
  filter_rehearsal:
    Argv|re: '^(--dry-run(=.*)?|--help|-h)$'
```

`CommandLine|contains: ' --help'` looks like a flag test and is not one: `--help-me`
satisfies it, and so does any argument value containing the text — the field the
model writes. Keep `CommandLine` for a genuinely multi-word phrase, and for a flag
inside a quoted script such as PowerShell's `-Command "… -WhatIf"`, which arrives
as a single token.

Do not add a flag whose meaning differs for the tool being matched. `-n` is
`--dry-run` for restic, borg and rclone, and `--namespace` for velero and kubectl;
scope such a filter with an `Image` key in the same selection.

## Field modifiers

Default: exact equality. Supported: `|contains`, `|startswith`, `|endswith`, `|re` (Go RE2), and `|all` combined with substring/prefix/suffix forms. Unsupported alone or combined forms (for example `|all` alone, `re+all`, `cidr`) mark the rule inactive.

## Condition grammar

Supported: `or`, `and`, `not`, parentheses, `1 of …`, `all of …`, `them`, selection names. Aggregations (`|`, `near`, `timeframe`, `count()`) are not supported.

## Levels and posture

Levels: `informational`, `low`, `medium`, `high`, `critical`.

| Level | Light | Balanced | Strict |
|---|---|---|---|
| `informational`, `low` | Ignored | Ignored | Ignored |
| `medium` | Ignored | Ignored | May ask / hold |
| `high` | Ignored | May ask / hold | May ask / hold |
| `critical` | May ask / hold | May ask / hold | May ask / hold |

A match may ask (command) or hold CONNECT (egress). It never auto-allows and never clears a structured boundary ask. Settings **never_ask** stands down asks including detection overlays.

## Validate and ship

Every path rehearses the same thing: each rule's positive cases must match through the production tool or egress adapter, and each negative must stay silent against **every** rule in the pack.

For a pack in the repository's security catalog:

```bash
./task test:digest -- ./internal/detectionpack/...
```

That digest discovers the shipped catalog; it does not validate a folder elsewhere on disk.

For a pack shipped inside an extension pack (`host/detection-packs/<pack_id>/`):

```bash
pw extensions validate
```

It compiles the whole catalog graph and rehearses every declared case, failing on a positive that did not match or a negative that did. Distribution is then ordinary extension distribution — install, version ranges, update, suites — and Settings lists the pack as **From an extension**, attributed to the pack id, with its own toggle.

For a device folder, Settings → Approvals → Detections → **Add pack…** performs the supported dry-run preview before confirmation. Inspect rules found, inactive/unsupported reasons, ignored files, and any rehearsal line. Confirmation copies an allowlisted set (`pack.yaml`, `rules/*.yml`, `fixtures.yaml`) into `{configdir}/detection-packs/<pack_id>/`; symlinks and other files are skipped. Iterate by editing that copy or re-importing with replace.

Rehearsal is an authoring gate, not a load gate: a pack that fails it still loads, because a rule that misses is silence and the floor already assumes silence. What it stops is shipping a coverage claim the rules do not meet.

Only an imported folder can be deleted here. A pack that ships with the app is turned off; a pack an extension provides is turned off here or uninstalled with that extension.
