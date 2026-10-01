# Dependency policy

How Painted Wolf Code handles its dependencies. These files are the source of truth;
[`docs/operations/dependency-inventory.md`](../docs/operations/dependency-inventory.md) and
[`.github/dependabot.yml`](../.github/dependabot.yml) are generated from them together with the
pins in the tree.

```bash
./task codegen:dependency-inventory            # regenerate after editing (offline)
./task codegen:dependency-inventory:upstream   # re-query registries into upstream.json, then regenerate
```

The `dependency inventory` GitHub workflow runs these for you: it regenerates the page when
dependencies or this directory change on main, and refreshes upstream versions weekly.
`./task check` runs `codegen:dependency-inventory:check`, which fails only when this policy
no longer matches the manifests or `.github/dependabot.yml` is stale. Run
`./task codegen:dependency-inventory` after editing the policy so `dependabot.yml` follows.
The default group includes minor and patch version updates. Majors and security updates
remain separate PRs; existing package holds still apply. Registry freshness is distinct from
vulnerability scanning; see the inventory's release-review runbook.

When adding a manifest, add its section to `policy.yaml` and its manifest and lockfile paths
to `.github/workflows/dependency-inventory.yml`. Add each declared pin source there too, so
pin changes refresh the page without waiting for the weekly registry query.

## Files

| File | Holds |
|---|---|
| `policy.yaml` | Page title, quadrant stances, Dependabot schedule, and the section render order |
| `intro.md`, `ratings.md`, `runbooks.md` | Page prose. Links resolve from `docs/operations/`, where the page is written |
| `sections/*.yaml` | One inventory section each: its manifest, groups, and entries |
| `upstream.json` | Generated snapshot of upstream versions; never edit by hand |

## Sections

A section either reads a **manifest**, which discovers every direct dependency on its own, or
lists **rows** declared by hand, or both.

```yaml
title: Frontend packages (`lycaon-den`)
intro: |
  Optional Markdown shown under the heading.
manifest:
  kind: bun                    # gomod | bun | cargo
  path: lycaon-den/package.json
  lock: lycaon-den/bun.lock    # bun and cargo only
  dependabot_group: bun-dependencies   # omit to leave this manifest out of Dependabot
groups:
  - title: Locally patched packages
    intro: |
      Optional Markdown shown under the group heading.
    packages:
      '@codemirror/view':
        urgency: low
        friction: high
        updates: hold
    rows: []
packages: {}                   # judgement for entries outside any group
rows: []                       # declared rows outside any group
```

A discovered dependency without an entry still appears, with blank judgement, under
**Other entries**. An entry for a package the manifest no longer lists is an error, so remove
judgement when you remove the dependency. Quote package names that start with `@`.

## Judgement fields

Every field is optional.

| Field | Values |
|---|---|
| `urgency` | `critical`, `high`, `moderate`, `low`; see `ratings.md` |
| `urgency_reason` | Markdown; rendered as **Urgency:** in the notes cell |
| `friction` | `high`, `moderate`, `low` |
| `friction_reason` | Markdown; rendered as **Friction:** |
| `notes` | Any other Markdown |
| `updates` | Manifest packages only. `hold` ignores every Dependabot update, `no-major` ignores major releases, and `patch-only` ignores major and minor releases. Ignore rules also stop Dependabot security PRs for that package. Blank keeps default grouped updates |

In the notes cell, a blank line starts a new paragraph.

## Declared rows

Rows cover what no manifest lists: runtimes, engines, tools, and vendored data.

```yaml
rows:
  - id: git-engine             # stable kebab-case key into upstream.json
    name: Dugite Git and Git LFS
    pins:
      - yaml lycaon/config/gitengine/pin.yaml git_version
      - label: LFS
        from: yaml lycaon/config/gitengine/pin.yaml lfs_version
    upstream:
      - 'github-tag git/git ^v\d+\.\d+\.\d+$'
      - github-release git-lfs/git-lfs
    urgency: critical
```

Each pin or upstream source is one line, `kind arguments…`, or `{label, from}` when the cell
needs a label. Upstream entries line up with pins by position; an upstream entry past the last
pin compares against the first pin. Quote a source that contains `\`, `^`, `$`, or `@`.

| Pin kind | Arguments | Reads |
|---|---|---|
| `file` | path | The trimmed file contents |
| `shell` | path, variable | `VAR="x"` or `VAR="${VAR:-x}"` in a shell script |
| `go-const` | path, name | A Go string constant |
| `toml`, `yaml`, `json` | path, dotted key | A scalar; list items by index, as in `vendors.0.commit` |
| `yaml-count` | path, dotted key | The length of a YAML list |
| `go-directive` | `go.mod` path | The `go` directive |
| `go-require` | `go.mod` path, module | A required module version |
| `npm-lock` | `bun.lock` path, package | The hoisted resolved version |
| `cargo-lock` | `Cargo.lock` path, crate | The highest locked version |

| Upstream kind | Arguments | Queries |
|---|---|---|
| `go-module` | module | `proxy.golang.org` latest |
| `npm` | package | npm `latest` tag |
| `crate` | crate | Newest stable, unyanked version in the crates.io index |
| `github-release` | repo, optional prefix to strip (default `v`) | Latest release tag |
| `github-tag` | repo, regular expression, optional prefix | Highest matching tag |
| `github-commit` | repo | Default-branch head |
| `go-release` | `latest` or `line` | Newest Go, or newest point release of the pinned minor |
| `node-release` | — | Newest Node in the pinned major |
| `rust-stable` | — | Current stable Rust |
| `chrome-for-testing` | channel | Chrome for Testing last-known-good version |

Set `GITHUB_TOKEN` before a refresh if GitHub's anonymous rate limit gets in the way.
