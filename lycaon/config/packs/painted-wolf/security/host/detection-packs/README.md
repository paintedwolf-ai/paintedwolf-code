# Detection packs

Bundled Sigma rules that may raise an **extra** approval ask (or hold a mediated CONNECT) on top of containment and approval posture. They are an overlay, not the safety floor.

Product SSOT: [`docs/security.md`](../../../../../../../docs/security.md) § Detection packs. Field vocabulary SSOT: [`internal/detectionpack/event.go`](../../../../../../internal/detectionpack/event.go). Authoring skill: [`write-detection-pack`](../../skills/write-detection-pack/SKILL.md) and its [`sigma-subset.md`](../../skills/write-detection-pack/references/sigma-subset.md).

## Layout

```
<pack_id>/
  pack.yaml
  rules/<slug>.yml
  fixtures.yaml
```

Each stable rule needs a fresh RFC 4122 UUID `id`, `title`, `level`, `logsource`, `author`, `references`, and ISO `date` / `modified` metadata, plus `detection` with at least one selection and `condition`. Caps and import rules are in the skill reference.

## Floor vs overlay

| Layer | Handles |
|-------|------|
| Floor | Containment (`Contained`), write roots, egress host rules, approval posture |
| Overlay | These packs — match → ask or hold CONNECT |

A pack never auto-allows, never clears a boundary ask, and never invents a deny. A miss is not proof of safety; the floor still applies.

Two packs are read a second time, as **data the floor consults** rather than as detections:

| Pack | Read by | Answers |
|------|---------|---------|
| `credential-stores` | `confine` | Which files earn a file-precise grant card instead of a directory one, and which directories may not be attached as a project root |
| `key-material` | `confine` | Which paths are refused with no grant offered at all, and may not be attached as a project root |

Both are loaded fail-closed at boot (`app.wireCredentialFloors`): a pack that fails to parse fails the boot rather than quietly making `~/.ssh` writable. User and project overlays extend the credential-store *asks* through the matcher; neither can extend or shrink the `key-material` floor, because taking the ladder away from a path is a decision the shipped pack makes.

## Tiers

| Tier | Location | May |
|------|----------|-----|
| Bundled | this directory, in the binary | Ship rules |
| Device | `<configDir>/detection-packs/<id>/` + `detection-packs.yaml` | Add packs, disable any pack |
| Project | `{repo}/<overlay>/detection-packs.yaml` | **Enable-only** — flip `enabled` on a pack the device already has |

The project tier mirrors the scanner catalogue (`internal/scan/catalog_merge.go`): rows carry `id` and `enabled` and nothing else, an unknown id is rejected rather than created, and the tier applies only when project scan configuration is on. A project cannot define a rule.

```yaml
# {repo}/<overlay>/detection-packs.yaml
packs:
  - id: payment-platform
    enabled: true
  - id: local-vm
    enabled: false
```

It applies to `tool_exec` only. A mediated CONNECT carries a session and a destination but never a project directory, so no project tier can attach to `egress_observed`.

## Log sources

Exactly two:

- `tool_exec` — pre-exec ask on an outer tool action
- `egress_observed` — CONNECT hold while a confined process dials

Do not invent fields. Closed sets are listed in `event.go` and contract-checked against `schemas/detection-rule.schema.json`.

## Tags vs level

Two independent axes. **Where the effect lands** decides whether a match is
gate-shaped for `authority_misuse`; **how big it is** decides which postures hear it.

| Tag | Means | Card says |
|-----|-------|-----------|
| `lycaon.effect.external` | An account or service beyond this machine | "reaches an external system" |
| `lycaon.effect.local` | This machine itself | "changes something on this machine" |
| `lycaon.effect.unrecoverable` | Cannot be put back | "… and cannot be undone" |

A rule declares **exactly one** of `external` / `local` — never both, because the
card can only state one place. `unrecoverable` is orthogonal and rides along.

- **Level** (`critical` / `high` / `medium` / …) decides which postures hear it: Light hears `critical`; Balanced also `high`; Strict also `medium`.
- `critical` and `high` **must** declare `unrecoverable`; `medium` **must not**. That is
  what keeps the reversible tier out of the default posture, and it is enforced by
  `TestDetectionPackLevelMatchesEffectClass`.

An untagged rule fails toward asking.

An `external` rule is stood down when the boundary denies egress — the effect has no
way out. A `local` rule is not: `rm -rf /` needs no network. What stands a local rule
down is `filter_local` on `EffectReach`, below.

## EffectReach

`EffectReach` is a **host-derived** `tool_exec` field: `local` | `remote` | `unproven`.

| Value | Meaning |
|-------|---------|
| `local` | Destination is loopback, or a `file:` / path registry under attached roots |
| `remote` | A concrete non-loopback host (including default public registries) |
| `unproven` | No destination derived — **default**; fail closed toward ask |

The host resolves destinations from argv flags and project config only to **obtain a URL**, then classifies it with OS address facts. A checkout that names a registry `local-registry` but points it at crates.io still yields `remote`.

### Opt in by default (noise is the enemy)

Bundled rules for effects on remote providers normally filter `EffectReach: local`:

```yaml
filter_local:
  EffectReach: local
condition: … and not filter_local
```

Silence is a **Sigma non-match**. The gate does not special-case `EffectReach`.
Reach is only `local` when the host can *prove* every destination the action names
is already inside the write jail, and anything it cannot prove stays `unproven`,
which asks.

That proof is what makes destructive work inside the attached project quiet:
`rm -rf node_modules`, `git reset --hard`, and `chmod -R` on a project path all
resolve `local`, because the boundary would already permit the write and the
person's own version control is the record. The same commands ask the moment they
name a path the jail does not cover.

Proving containment is deliberately hard to do by accident. A token carrying a
variable, a substitution, a brace expansion, or an `xargs` placeholder is
unreadable here and stays `unproven` — `rm -rf $HOME/x` is not a project path, and
`rm -rf /*` names the root rather than the project.

Local host-integrity and credential-store rules may intentionally omit that filter;
`egress_observed` rules do not use it because they have a different log source.

### What the host marks `local` today

- `awslocal`, or `--endpoint-url` / `--endpoint` / `--base-url` to loopback
- structured `endpoint_url` / `endpoint` / `base_url` arguments to loopback
- `AWS_ENDPOINT_URL*` / `LOCALSTACK_HOST*` in the action env pointing at loopback
- Cargo / npm / PyPI publish or yank/unpublish to a loopback or in-root `file:` registry
- npm/pip/uv registry redirect flags (`--registry`, `--index-url`, `config set registry`, …) to loopback
- `GOPROXY=…` to loopback
- every path operand of `rm`, `rmdir`, `unlink`, `shred`, `truncate`, `find`,
  `chmod`, `chown` and `chgrp` resolving inside the write roots
- a `git clean` / `reset` / `checkout` / `restore` whose work tree is inside them
- Container image push to `localhost` / `127.0.0.1` registries

Grow the resolver before inventing new argv denylists. Do not reintroduce LocalStack-only `CommandLine|contains` filters in rule YAML.

## Rehearsal filters

A rehearsal filter matches **argv tokens**, not command-line substrings:

```yaml
  filter_rehearsal:
    Argv|re: '^(--dry-run(=.*)?|--help|-h)$'
```

`CommandLine|contains: ' --help'` reads as a flag test and is not one. It is
satisfied by `--help-me`, and by any argument *value* that happens to contain the
text — which is the field the model writes. Anchoring on a whole argv token is
what makes the filter mean what it says.

Two shapes still belong on `CommandLine`: a genuinely multi-word phrase
(`' policy read'`), and a flag inside a quoted script, since PowerShell delivers
`-Command "… -WhatIf"` as a single token.

Never add a flag to a filter that means something else for the tool being matched.
`-n` is `--dry-run` for restic, borg and rclone, and `--namespace` for velero and
kubectl. Scope it with an `Image` key in the same selection.

## Structured cloud actions

An MCP server fronting AWS takes `action: "s3:DeleteBucket"` where the CLI would
have been `aws s3api delete-bucket`. The host projects that identity into
`ApiAction`, the same field the CLI form produces, so one reviewed rule governs
both surfaces. AWS rules pair `Image: aws` with a `Tool|startswith: 'mcp_'` arm
and carry a `filter_structured_rehearsal` for the argument form of `--dry-run`.

GCP and Azure rules match command text rather than an operation identity, so their
structured coverage comes from the generated bridge packs instead.

## Fixtures

`fixtures.yaml` lists positives and negatives per rule slug. Each entry may be:

- a bare command string, which enters the real `ProposedAction → GateSource` path, or
- a structured tool action with the registry-authored identity and actual arguments:

```yaml
positive:
  - tool: mcp_payments_create_charge
    approval_category: mcp
    approval_subject: payments.create_charge
    args: {amount: 2500, customer_id: cus_123}
```

Every rule needs at least one positive and one negative. Positives must match through
the production gate or egress adapter—fixtures cannot manufacture `EffectReach` or
other host facts. Negatives are checked against **every** rule in the pack.

`detection-approval-corpus.yaml` is the cross-pack noise budget. Its cases enter the
same production adapter and pin default-posture routine reads, local endpoints,
rehearsals, generic verbs, and near-miss registry subjects as silent. It also pins
representative asks and the one rule that must win, so a new higher-severity overlap
cannot quietly change the card.

## Structured tools

`ToolArg` contains bounded `name=value` projections for native structured and MCP
tools. Match explicit keys or values there; never relabel them as `CommandLine`.
Bundled generic verbs do not create approvals. Cloud operation identities are covered
by the surface rules described above rather than by a `ToolArg` match.
The trusted `detection-action-semantics.yaml` catalog maps registry-authored MCP
subjects to closed consequence fields such as `ActionEffect`, `TargetScope`, and
`CredentialPersistence`. Every subject/tool regular expression must anchor the
complete registry identity. A device may add provider-specific mappings in
`<configdir>/detection-action-semantics.yaml`; mappings are additive only.
Rules for native actions may pair an exact `Tool` identity with normalized
`TargetFile` paths already supplied by the approval adapter. Use anchored path
categories and mutation-tool allowlists; reading a sensitive path or writing a
lookalike project path must remain silent.

Command and verify pipelines use the same canonical command projection as execution,
so `PipelineCommandLine` rules see both string commands and structured `pipeline`
arrays without flattening away stage order.

## Authoring loop

From the repo root:

```bash
./task test:digest -- ./internal/detectionpack/...
```

That loads the bundled catalog, validates fixtures, and exercises matchers. Device packs (Settings → Approvals → Detections → Add pack) are copied into the config directory; built-ins can only be turned off.

## Equivalents

Packs may list drop-in binary names (`pnpm` for `npm`, `tofu` for `terraform`) for Settings copy (“Also covers …”). Rules name `Image` values explicitly.

`egress-providers` holds CONNECT on the common developer internet at medium: cloud control planes, data platforms (warehouses, OLTP, search/vector, ETL), streams/queues, specialist object storage (R2/B2/Wasabi), CRM/customer-data APIs, forge APIs, container registries, package publish endpoints, CI/CD, secrets/auth SaaS, and everyday developer SaaS. Git apexes, mass install registries, and arbitrary CDN edges stay quiet on purpose.

The same pack also holds two rules outside that coarse band: public webhook/OOB-interaction collectors (`high` — no legitimate read side, so the hostname alone is a precise match, the same logic that keeps instance-metadata at `critical`) and anonymous paste/file-drop services (ordinary coarse `medium`, since those do have a legitimate read side). `exfil-sinks` matches the same two provider tiers on `tool_exec` — command line, `ToolArg`, or `DeclaredDestination` — for the destinations a CONNECT hold alone cannot: a raw-IP dial where only the declared hostname names the sink, and an ask ahead of the dial rather than during it.
