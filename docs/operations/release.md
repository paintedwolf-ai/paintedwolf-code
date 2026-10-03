# Release checklist

Public launch target: macOS Apple Silicon (`packaging/release-platforms.json` marks
it `public`; Linux and Windows are `candidate`). Prereleases such as `1.0.0-rc.1`
go to Preview; versions such as `1.0.0` go to Stable.

## One-time setup

- [ ] Create the public source repository `paintedwolf-ai/paintedwolf-code` and
  the Homebrew package repository `paintedwolf-ai/homebrew-tap`.
- [ ] Flatten the Git history into a clean initial public release commit, signed
  and signed-off for DCO (`git commit -S -s`). Push it as the initial `main`.
- [ ] Create the R2 bucket and connect `downloads.paintedwolf.dev`. Serve
  versioned artifacts unchanged; bypass caching for `/updates/*/latest.json`.
- [ ] Set repository variable `DOWNLOAD_BASE_URL` to
  `https://downloads.paintedwolf.dev`; the workflow refuses any other value.
- [ ] Create the GitHub environments below and configure their credentials.
  Allow release tags for publication and `main` for the halt workflow.

| Environment | Secrets | Variables |
|---|---|---|
| `release-signing` | `APPLE_CERTIFICATE` (base64 p12), `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY`, `APPLE_ENGINE_PROVISIONING_PROFILE_BASE64`, `APPLE_API_KEY_P8` (base64), `APPLE_API_ISSUER`, `APPLE_API_KEY_ID`, `TAURI_SIGNING_PRIVATE_KEY`, optional `TAURI_SIGNING_PRIVATE_KEY_PASSWORD`; Windows candidates also need `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TENANT_ID` | Windows candidates: `AZURE_ARTIFACT_SIGNING_ENDPOINT`, `AZURE_ARTIFACT_SIGNING_ACCOUNT`, `AZURE_ARTIFACT_SIGNING_PROFILE` |
| `release-publication` | `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN`, `R2_BUCKET`, `HOMEBREW_TAP_TOKEN`, `WWW_DISPATCH_TOKEN` | `HOMEBREW_TAP_REPO=paintedwolf-ai/homebrew-tap` |
| `release-rehearsal` | `RELEASE_TEST_R2_API_TOKEN` | `RELEASE_TEST_R2_ACCOUNT_ID`, `RELEASE_TEST_R2_BUCKET`, `RELEASE_TEST_DOWNLOAD_BASE_URL` |

The `release-rehearsal` environment serves the weekly [release system live
test](../../.github/workflows/release-system-live-test.yml), which exercises
the R2 controls through an isolated `release-system-tests/` prefix; it may
reuse the public bucket and domain or point at a scratch bucket.

- [ ] Use an Apple Developer ID certificate and notarization key. Match the
  updater private key to the public key in
  [update-keys.json](../../packaging/update-keys.json); keep a recovery copy.
- [ ] Give each token **Contents: read and write** access to its repository:
  `HOMEBREW_TAP_TOKEN` for the tap and `WWW_DISPATCH_TOKEN` for
  `paintedwolf-ai/paintedwolf-www`.
- [ ] In the website repository, set secrets `CLOUDFLARE_API_TOKEN` and
  `CLOUDFLARE_ACCOUNT_ID`, plus variables `CLOUDFLARE_PAGES_PROJECT_NAME` and
  `CLOUDFLARE_ZONE_ID`. Merge the website automation and run its deployment once.
  Bypass Cloudflare caching (`Cache-Control: no-store`) for
  `/.well-known/releases/*` and `/download/`, and confirm `/download/` serves
  `<a download data-release-channel="{channel}" ...>` links.
- [ ] Confirm the site has working download, support, privacy, and license links.
- [ ] Publish and select the engine revision with Linux artifacts needed
  by Ubuntu CI. See [engine selection](#engine-selection).
- [ ] Enable **Private vulnerability reporting** in repository settings and
  install the [DCO GitHub App](https://github.com/apps/dco) to enforce sign-offs.
- [ ] Protect `main` with the `main-protection` ruleset: pull requests merged by
  squash through a merge queue, the required checks `check` (GitHub Actions) and
  `DCO`, no force-pushes or deletions, and the dependency-inventory deploy key
  as the only bypass actor. `check` is the aggregate of every CI tier, so no
  individual job is listed.
- [ ] Configure public repository presentation: description (`Local-first AI coding agent`), website (`https://paintedwolf.ai`), topics (`ai`, `agent`, `tauri`, `golang`, `solidjs`, `local-first`), and social preview image.

## Release

Release builds and desktop CI automatically download the public Bialy heads
from `paintedwolfcode/bialy` at the immutable revision pinned in
[`setup-bialy`](../../.github/actions/setup-bialy/action.yml). The action verifies
their SHA-256 hashes and model metadata against the host's
[`decision-release.json`](../../lycaon/config/packs/painted-wolf/platform/host/decision-release.json)
before making them available to packaging through `BIALY_HEADS_DIR`. No Hugging
Face token or pre-existing local head cache is needed. When selecting a new head
release, update both the host manifest and the action's revision pin together.

1. Update `VERSION`, increment `RELEASE_BUILD`, and update `CHANGELOG.md`.
   Synchronize desktop versions with `bash scripts/sync-den-versions.sh`.
2. Prepare the release fixture and check the candidate:

   ```bash
   ./task upgrade:corpus:prepare
   ./task release:preflight -- --require-corpus
   ```

   Commit the version changes and generated fixture with the candidate.
   Pre-v1 schema changes redefine revision 1 and refresh its locks; no migration
   is needed ([compatibility](../compatibility.md)).
3. If provider integrations changed, run the [provider checks](#provider-integration-checks).
4. Merge the candidate through the merge queue, which runs the full CI tier on
   the commit that lands, and wait for
   [Release build cache](../../.github/workflows/release-build-cache.yml) to
   pass; the release build restores the Go and Tauri compiles that run saved.
   Tag that exact commit as `v<VERSION>` and push the tag. This starts
   [Release](../../.github/workflows/release.yml), which refuses a commit
   without a passing full-tier `CI/check`. A dependency-inventory refresh
   pushed after the candidate bypasses the queue: dispatch
   [CI](../../.github/workflows/ci.yml) on `main` and tag once it passes.
5. The workflow tests, builds, signs, notarizes, publishes the downloads,
   updates the tap and website, and activates the updater feeds. Manual dispatch
   never publishes.
6. Download the public app on a clean Mac. Open it, configure a provider, attach
   a project, complete a turn, then quit and relaunch. If a previous public
   release exists, also check updating from it and preserving local state.
7. Check the website download, Homebrew cask, updater feed, and GitHub release
   show the intended version, and keep the workflow run link with the release
   notes. Published versions are immutable; corrections use a new version.

## Automatic-update qualification

The first release containing the automatic installer still requires the old client's manual update (or Homebrew). Later direct-download clients discover, stage, and install without visiting Settings. Candidate Linux and Windows entries remain unpublished until their activation adapters receive equivalent qualification.

Local tests establish state, migration, archive, and transaction invariants. A signed beta and clean VMs must establish the packaged lifecycle before promotion:

1. Install a released app A at a writable application location. Exercise chats, drafts, an editor, and multiple windows without opening update settings.
2. Offer signed beta B through the release channel; verify background staging and the persistent restart action. Ordinary quit must stop the engine before exchange, install B, and remain closed. The next launch must run B and preserve released history and preferences.
3. Repeat with explicit restart, opt-out during download and after staging, channel change, a Homebrew receipt, a non-writable application directory, low disk space, and offline quit. Automatic work must never raise an administrator prompt or initiate restart.
4. Withdraw B after download. Its final offer check must prevent activation. Replace it with C and verify that C does not inherit B's staged identity.
5. Interrupt the helper before exchange, after exchange, and before the completion journal write; launch concurrently with activation. Both complete bundles must remain identifiable, and no process may start an engine from mixed installation files.
6. Exercise a supported released-store migration and its recovery snapshot. A failed new-version startup must retain recovery evidence, never downgrade the database or repeatedly relaunch.
7. Test a skipped signing-key bridge and a second restart into the successor release. Inspect version-bound signatures and package signing on both sides.

Keep results with the release evidence. A local unit pass is not a claim of Gatekeeper, application-translocation, logout, power-loss, or cross-account filesystem behavior; these belong in the beta/VM qualification.

## If a release needs to be stopped

1. Run [Release halt](../../.github/workflows/release-halt.yml) with `bad_version`.
   Set `last_good_version` to restore an older release, or leave it empty to stop
   offering downloads when there is no safe replacement.
2. Review the default dry-run plan, then run it with `dry_run=false`. It updates
   all affected feeds and withdraws or replaces the tap and website downloads.
3. Verify the public download state. Existing installations are not downgraded.
   Publish a fixed version and explain the affected versions and recovery steps.

## When updating dependencies

### Engine selection

Build and publish engines in
[paintedwolf-opengrep](https://github.com/paintedwolf-ai/paintedwolf-opengrep).
Select a published release here, then verify it against the application's rules:

```bash
./task scan:opengrep:select -- --tag "$ENGINE_TAG" --expected-commit "$ENGINE_COMMIT" "https://github.com/paintedwolf-ai/paintedwolf-opengrep/releases/download/$ENGINE_TAG/release.json"
./task test:lycaon-rules
./task test:scanners
```

Commit the updated pin and attestation receipt together. Application packaging
preserves the signed engine and notarizes the final app. See
[scanner supply chain](../scan-supply-chain.md#bundled-opengrep-selection).

### macOS credential host signing

The Go host owns credential decryption and ships in
`Contents/Helpers/Painted Wolf Code engine.app`, bundle identifier
`dev.paintedwolf.code.engine`. It needs its own **Developer ID** provisioning
profile for that explicit identifier, with Keychain access enabled and the
release's Developer ID Application certificate included. The profile must
authorize the host's application-identifier Keychain group; packaging takes the
identifier prefix and team from the profile and grants only that private group.

Store the base64-encoded profile as `APPLE_ENGINE_PROVISIONING_PROFILE_BASE64`
in the `release-signing` environment. CI decodes it to a temporary file and
sets `APPLE_ENGINE_PROVISIONING_PROFILE` to that path; local release builds set
the path variable and `APPLE_SIGNING_IDENTITY` themselves. Packaging
(`scripts/stage-macos-host.py`) rejects absent, expired, development,
wrong-identifier, wrong-group, and wrong-signer profiles. Renew the profile
before expiry and after changing the signing certificate.

`stage-engine.sh` constructs and signs the helper before Tauri copies it into
`Contents/Helpers`, and Tauri's custom-file packaging preserves that nested
signature. Do not re-sign the helper as a generic external binary: that drops
its restricted entitlements. The renderer and desktop shell receive no vault
identity export API. The installed `pw` link targets this helper, and resource
discovery follows the link back to the outer app's
`Contents/Resources/engine-root`, so CLI commands use the same bundled browser,
Git engine, and scanner as the desktop app.

Packaging, the signed bundle audit, and the release smoke test run
`pw credentials verify-protection` from the helper. It accesses only randomly
named disposable Keychain items, verifies their protection and isolation, and
deletes them; it never reads or resets the user's vault. A failed probe blocks
the release. On GitHub-hosted runners the launch smoke only warns, because GUI
launch there is unproven; until a real Mac runs in the pipeline, qualify the
published build with `bundle-smoke.sh` on a real Mac. These checks need an unlocked interactive
macOS user session and valid signing inputs; development, test, and performance
hosts cannot qualify.
Unsigned development builds (`./task den:app -- --debug`) use the separate
development identity backend and cannot qualify this boundary.

### Code signing and runtime code generation

Every executable in the macOS bundle runs under the hardened runtime, and
`bundle:verify` holds each to an exact entitlement contract: the engine helper
carries only its provisioning-profile entitlements, `chrome-headless-shell` carries
exactly `allow-jit` and `allow-unsigned-executable-memory`, and every other
executable, including `pw-document-core`, carries none. An executable that
generates machine code at run time is killed by the kernel the first time it
runs that code, so nothing built from this repository may: the release Go graph
links no WebAssembly runtime (`TestShippedGoBinariesLinkNoRuntimeCodeGenerator`),
the document core is native, and development builds are hardened too, so such a
dependency fails in development first. The signed path is proven by running it:
`bundle:verify` runs `pw diagnostics document-core` from the packaged engine, which
confines the sibling core and round-trips a document; on macOS, `test:full` edits a
document through an ad-hoc hardened engine; and Linux and Windows staging run the
same probe against their packaged binaries.

### Provider integration checks

After changing adapters, reasoning controls, or tool-call replay, run the checked-in
[reference plan](../../scripts/coordinator-benchmark/provider-checks.json) with your
private provider configuration:

```bash
./task build:lycaon-dev
./task eval:tool-usage BENCHMARK=providers -- --allow-live \
  --source-config /absolute/path/private-provider-config \
  --out /absolute/path/provider-checks
```

This makes paid provider calls. Resolve failed or inconclusive results before
shipping the changed integration. The coordinator benchmark runs independently
and is not a release gate.
