# Licensing

Apache-2.0 for application code; CC-BY-4.0 for first-party catalog YAML under `lycaon/config/` (workflows, rules, hints, and related packs, not OpenAPI, Taskfiles, or other repo YAML); upstream licenses for bundled engines, vendored rules, and design-kit assets; Painted Wolf marks reserved.

**See also:** [Dependencies](dependencies.md) · [Scan supply chain](scan-supply-chain.md) · [Naming](naming.md) · [Trademarks](trademarks.md) · [Contributing](../CONTRIBUTING.md)

## License map

| Artifact | License |
|----------|---------|
| Go backend `lycaon/`, including `internal/scan` and sandboxed execution | **Apache-2.0** ([`LICENSE`](../LICENSE)) |
| Den `lycaon-den/` | **Apache-2.0** |
| First-party catalog YAML (scan, workflow, posture, policy) | **CC-BY-4.0** ([below](#painted-wolf-code-rules-cc-by-40)) |
| Bundled in-repo scan gate rules | **CC-BY-4.0** (`origin: bundled-in-repo`) |
| Bundled OpenGrep binary | **LGPL-2.1** (upstream) |
| Linked gitleaks / osv-scalibr | **MIT** / **Apache-2.0** (upstream) |
| Bundled git / git-lfs | **GPL-2.0** / **MIT** (upstream) |
| Vendored gate rules | **MIT** (upstream, per vendor catalog) |
| Vendored Kingfisher secret definitions | **Apache-2.0** (upstream) |
| Hermetic `render_view` design kit | **OFL-1.1** fonts + **ISC** Lucide-adapted icons ([below](#hermetic-render_view-design-kit)) |
| Painted Wolf names, marks, and artwork | Trademark, Painted Wolf LLC; limited theme alterations under the [trademark policy](trademarks.md) |
| Inbound contributions | **DCO** ([below](#contributions)) |

## Trademarks

Painted Wolf LLC retains all rights in **Painted Wolf**, **Painted Wolf Code**, product logos, mascots, UI artwork, and other brand assets. Apache-2.0 grants copyright permissions for source code; it does not grant trademark rights. Redistributor rules, including the limited permission for themes to alter the fixed identity's theme-controlled colors and logo-glyph visibility, are in [Trademarks](trademarks.md).

## Application source (Apache-2.0)

The Go backend (`lycaon/`), desktop frontend (`lycaon-den/`), and other first-party application code are licensed under the Apache License, Version 2.0: the sidecar binary, API server, session/workflow/delegation logic, tool host, local scan stack, sandboxed execution, and UI shell. Root license text: [`LICENSE`](../LICENSE).

## Bundled OSS engines (upstream licenses)

Painted Wolf Code bundles or links these open-source components at build time. Each retains its own license; compliance obligations apply to the component, not to first-party rule YAML.

| Component | Role | License | Where documented |
|-----------|------|---------|------------------|
| [OpenGrep](https://github.com/opengrep/opengrep) | SAST engine (bundled binary); gate SAST uses OpenGrep only | **LGPL-2.1** | [`scan-supply-chain.md`](scan-supply-chain.md) |
| [gitleaks](https://github.com/gitleaks/gitleaks) | Secrets (Go library) | **MIT** | [`scan-supply-chain.md`](scan-supply-chain.md) |
| [osv-scalibr](https://github.com/google/osv-scalibr) | SCA (Go library) | **Apache-2.0** | [`scan-supply-chain.md`](scan-supply-chain.md) |
| [git](https://github.com/git/git) | Version control engine (bundled binary) | **GPL-2.0** | [`git.md`](git.md) · [`dependencies.md`](dependencies.md) |
| [git-lfs](https://github.com/git-lfs/git-lfs) | Large-file filter for the bundled git (bundled binary) | **MIT** | [`git.md`](git.md) · [`dependencies.md`](dependencies.md) |

Pinned scanner versions, SHA256 checksums, and embedding notes: [`lycaon/config/runtime/scanners/bundled-manifest.yaml`](../lycaon/config/runtime/scanners/bundled-manifest.yaml). The Git toolchain is pinned in [`lycaon/config/gitengine/pin.yaml`](../lycaon/config/gitengine/pin.yaml) and staged into the bundle by `scripts/den-build-bundle.sh`; that staged tree is gitignored, so no dependency manifest sees it and its entries are carried by [`licensing/bundled-binaries.yaml`](../licensing/bundled-binaries.yaml). Git Credential Manager is pruned during fetch and does not ship.

## Vendored third-party rules (upstream licenses)

Security gate rules under `lycaon/config/runtime/scanners/rules/vendor/` are copies of upstream projects; each subtree includes the upstream `LICENSE`. Provenance (repo URL, pinned commit, `tree_sha256` of the vendored bytes, license) is recorded in [`rules-provenance.yaml`](../lycaon/config/runtime/scanners/rules-provenance.yaml) and verified offline by `./task scan:rules:vendor:check`: [Vendored gate rules](scan-supply-chain.md#vendored-gate-rules).

[Kingfisher](https://github.com/mongodb/kingfisher) secret definitions under `lycaon/config/runtime/scanners/secret-rules/vendor/kingfisher/` retain their Apache-2.0 license. The subtree includes upstream `LICENSE` and `NOTICE`; its `manifest.yaml` pins v1.110.0 and commit `484f860400bf9b47fff55f1a4379875dd98c792a`, locks the source digest, and records the import/exclusion inventory.

The Dropbox [zxcvbn](https://github.com/dropbox/zxcvbn) passwords frequency list (MIT) is vendored under [`secret-mint/vendor/zxcvbn-passwords/`](../lycaon/config/packs/painted-wolf/security/host/secret-mint/vendor/zxcvbn-passwords) with upstream `LICENSE`; [`zxcvbn-provenance.yaml`](../lycaon/config/packs/painted-wolf/security/host/secret-mint/zxcvbn-provenance.yaml) pins it and `./task secret-mint:vendor:check` verifies it offline.

## Scan gate rule origins

OpenGrep gate rules are loaded by origin (provenance and merge order), not by quality tier:

| Origin | Path / config | License | Notes |
|--------|---------------|---------|-------|
| **Vendor** | `lycaon/config/runtime/scanners/rules/vendor/` | Upstream (MIT in current inventory) | Pinned commit + vendored-tree digest in `rules-provenance.yaml` → `vendors[]` |
| **Bundled in-repo** | `lycaon/config/runtime/scanners/rules/lycaon/` | **CC-BY-4.0** | First-party security rules; `rules-provenance.yaml` → `lycaon.origin: bundled-in-repo` |
| **Project** | `{project}/.paintedwolf/opengrep-gates.yaml` | Your choice | Append-only paths; OpenGrep-compatible YAML |

## Painted Wolf Code rules (CC-BY 4.0)

Original rule and policy YAML written for Painted Wolf Code, not vendored from third parties, is licensed under [Creative Commons Attribution 4.0 International](https://creativecommons.org/licenses/by/4.0/):

| Path | Content |
|------|---------|
| `lycaon/config/runtime/scanners/rules/lycaon/` | Bundled in-repo OpenGrep gate rules (`lycaon.{lang}.*` ids) |
| `lycaon/config/packs/painted-wolf/*/policy/` | OAR policy rules, one file per `Code:` |
| `lycaon/config/packs/painted-wolf/*/workflows/` | Bundled workflow manifests |
| `lycaon/config/packs/painted-wolf/platform/host/posture-rules/` | Posture / session rule packs |
| Other first-party catalog YAML under `lycaon/config/packs/painted-wolf/` | Guidance templates, agent personas, tool catalogs, playbooks |

License text: [`lycaon/config/runtime/scanners/rules/lycaon/LICENSE`](../lycaon/config/runtime/scanners/rules/lycaon/LICENSE). Provenance: `rules-provenance.yaml` → `lycaon.license: CC-BY-4.0`, `lycaon.origin: bundled-in-repo`; `lycaon` there is the engine codename used as a machine key ([Naming](naming.md)), not a product name. You may share and adapt these rules with attribution. CC-BY applies to the YAML listed above, not to the Apache-2.0 application code.

## Hermetic render_view design kit

`render_view` injects a host-bundled, offline design kit (no CDN fetches at rasterize time). Third-party assets retain their upstream licenses.

| Asset | License | Attribution | Location |
|-------|---------|-------------|----------|
| Curated OFL variable fonts (Inter, Source Serif 4, Source Sans 3, JetBrains Mono, Fraunces, Space Grotesk, DM Sans, Playfair Display, IBM Plex Sans, Literata) | **SIL OFL 1.1** | Required (reserved font names) | `lycaon/internal/browser/designkit/fonts/` + `licenses/OFL-*.txt` |
| Curated Lucide-adapted stroke icons | **ISC** | Copyright notice retained | `designkit/icons_data.go`, `icons.go` + `licenses/Lucide-ISC.txt` |
| Host shell CSS / tokens / viewport presets | **Apache-2.0** (first-party) | — | `designkit/shell.css`, `viewport.go` |

Machine-readable provenance (family, upstream URL, license file, source URL): [`designkit/provenance.yaml`](../lycaon/internal/browser/designkit/provenance.yaml).

## Icon and UI chrome design skills

The `craft-icons-and-chrome` skill (`lycaon/config/packs/painted-wolf/browser/skills/craft-icons-and-chrome/`) provides procedures, reference guides, and specimen vector and HTML assets (`assets/*.svg`, `assets/*.html`) for building vector icons and web UI chrome. First-party specimen assets and documentation are Apache-2.0. The reference guides analyze permissively licensed open-source icon and UI design systems (Lucide [ISC], Heroicons [MIT], Phosphor [MIT], Tabler [MIT], Radix [MIT], shadcn/ui [MIT], Shoelace [MIT]); upstream projects retain their copyrights and licenses.

## Distribution notices

Ship builds include a generated `THIRD-PARTY-NOTICES.md` (repo root, gitignored) aggregating Go modules (`go-licenses`), Den production npm packages, Tauri crates (`cargo-about`), [`licensing/bundled-binaries.yaml`](../licensing/bundled-binaries.yaml), and [`licensing/bundled-rules.yaml`](../licensing/bundled-rules.yaml). Generate it with `./task licenses:notices`; CI, nightly, and the release workflow run it, `den-build-bundle.sh` generates it when missing, and `tauri.conf.json` bundles it as an app resource. About → **Third-party software** opens that file. Unknown or unclassifiable licenses fail generation.

## Contributions

Inbound contributions use the [Developer Certificate of Origin](https://developercertificate.org/): every commit carries `Signed-off-by`, and a GitHub DCO check enforces the trailer on every pull request ([`CONTRIBUTING.md`](../CONTRIBUTING.md)). Contributors retain copyright in their submissions; contributions intentionally submitted for inclusion are under Apache-2.0 per the license and DCO. Published copyright lines may read `Copyright Painted Wolf LLC and contributors`; notable human authors are listed in [`AUTHORS`](../AUTHORS). Painted Wolf names and marks remain trademarks of Painted Wolf LLC ([Trademarks](#trademarks)).
