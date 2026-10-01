### Update automation

`.github/dependabot.yml` is generated from the policy. Each manifest section becomes one weekly
ecosystem block with the configured cooldown. Minor and patch version updates share a group;
major updates remain individual PRs. Security updates remain independent of that group and
the version-update cooldown. A 0.x minor update can still be breaking and needs review.
A package's `updates` value becomes an
ignore rule: `hold` ignores every update, `no-major` ignores major releases, and `patch-only`
ignores major and minor releases. Ignore rules also suppress Dependabot security updates for
that package, so held packages rely on the vulnerability gates and the behind-upstream list.

Dependabot does not cover runtime version files, CLI tool pins in `scripts/`, the engine pins,
or vendored catalogs; the behind-upstream list covers them.

The `dependency inventory` workflow keeps this page current without manual steps. It
regenerates the page when dependency manifests, declared pin sources, or `dependencies/` change on main, re-queries
upstream versions every Monday, and commits only generated files. `./task check` verifies only
what people edit: that `dependencies/` still matches the manifests and that
`.github/dependabot.yml` is current. Dependabot PRs therefore pass without a regeneration
commit.

For a release review, run `./task codegen:dependency-inventory:upstream`, then
`./task codegen:dependency-inventory:check lint:vuln:fresh`. The inventory compares versions;
it does not establish vulnerability status or upgrade compatibility. The live vulnerability
gate covers Go only, not npm packages, Rust crates, or bundled engines. Review their upstream
advisories separately. Missing snapshot entries display `not refreshed` until the next
successful upstream refresh. After Go bumps, refresh the vendored database before closeout.

### Vulnerability triage (priority 1)

1. **Watch the nightly scan.** `nightly.yml` runs `./task lint:vuln:fresh` against the live
   database. It catches disclosures published since the pinned snapshot, for modules and the
   standard library alike; the offline `./task lint:vuln` gate covers everything up to the pin.
2. **Triage each disclosure.**
   * **Fixed upstream:** bump the module in `lycaon/go.mod`, or the `go` directive for the
     standard library. Re-vendor with `./task lint:vuln:vendor`, then verify with
     `./task lint:vuln`.
   * **Unreachable or no fix:** record the call-path analysis and a `reviewed` date in
     `lycaon/govulncheck-allowlist.yaml`, following the existing `docker/docker` entries.
3. **Respond to engine CVEs.**
   * **Git:** follow the [bump procedure](../dependencies.md#bump-procedure) with a fixed
     dugite release.
   * **Chrome:** update the version and hashes in
     `lycaon/internal/browserengine/chrome_pin.go`. Provision with `./task browser:ensure`, then
     re-verify confinement with `./task test:seatbelt`.
   * **Opengrep:** cut a fork release, authenticate it with `./task scan:opengrep:select`, then
     run `./task test:scanners`.

### Routine maintenance (priority 2)

* **Low-friction bumps:** take Dependabot's grouped PRs, plus low-friction rows that Dependabot
  does not cover. Verify with `./task check-fast`.
* **Tooling bumps:** move `golangci-lint`, `deadcode`, `oasdiff`, `sqlc`, and `task` together
  with Go releases, and fix any new lint findings in the same change.

### Held and patched dependencies (priority 3)

* **`@codemirror/view` and `overlayscrollbars`:** held. When reviewing a candidate:
  1. Read the upstream changelog to see whether it fixes natively what the local patch covers.
  2. Rebase the patch onto the candidate version and update the `patchedDependencies` key.
  3. Run `./task den:test`. Then run `./task den:stage-engine` followed by
     `./task e2e:den:desktop` on macOS to catch scroll and layout regressions in WebKit.
* **`yjs` and `yrs`:** held. Bump both in one change and test end to end against the WASM
  core.
* **`gotreesitter`:** follow [`third_party/README.md`](../../third_party/README.md), and drop
  each patch that upstream has absorbed.
* **Charm v2, `ureq` 3, and TypeScript 7:** keep the current majors until the migration is
  scheduled as its own work.
