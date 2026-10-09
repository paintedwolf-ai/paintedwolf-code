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
| `release-publication` | `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN`, `R2_BUCKET`, `HOMEBREW_TAP_TOKEN`, `WWW_DISPATCH_TOKEN`, `FEED_SIGNING_KEYS_JSON` | `HOMEBREW_TAP_REPO=paintedwolf-ai/homebrew-tap` |
| `release-rehearsal` | `RELEASE_TEST_R2_API_TOKEN` | `RELEASE_TEST_R2_ACCOUNT_ID`, `RELEASE_TEST_R2_BUCKET`, `RELEASE_TEST_DOWNLOAD_BASE_URL` |

Before enabling these workflows, replace the publication environment's two unnumbered feed secrets with `FEED_SIGNING_KEYS_JSON`:

```json
{"format_version":1,"generations":{"1":{"private_key":"<generation-1 private key>","password":""}}}
```

Retain every generation's feed credential while its feed may need a halt; add a separate entry before publishing a bridge. Each signing invocation receives only its selected credential. Build jobs never receive the map. The registered public key is checked even in rehearsals, which use an explicit fixture registry restricted to `release-system-tests/`.

The `release-rehearsal` environment serves the weekly [release system live
test](../../.github/workflows/release-system-live-test.yml), which exercises
the R2 controls through an isolated `release-system-tests/` prefix; it may
reuse the public bucket and domain or point at a scratch bucket.

- [ ] Use an Apple Developer ID certificate and notarization key. Match the
  artifact and feed private keys to `public_key` and `feed_public_key` in
  [update-keys.json](../../packaging/update-keys.json); keep recovery copies.
  Custody rules: [Signing custody](../dev-tasks.md#updates).
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
- [ ] Enable **Private vulnerability reporting** in repository settings. Configure
  repository-owned [DCO certification](#dco-certification).
- [ ] Protect `main` with the `main-protection` ruleset: pull requests merged by
  squash through a merge queue, the required contexts `check` (GitHub Actions check) and
  `DCO-owned` (GitHub Actions commit status), no force-pushes or deletions, and the dependency-inventory deploy key
  as the only bypass actor. `check` is the aggregate of each CI tier, so no
  individual job is listed: a pull request's fast tier admits it to the queue,
  and its merge group's integration gate admits it to main. Apply the reviewed
  [queue parameters](../test-strategy.md#determinism-and-capacity), two groups
  of one pull request each with `ALLGREEN`, whose gate caps leave the
  [runner capacity](../test-strategy.md#runner-capacity) for the other
  classes. Enable **Allow auto-merge**: it is how a ready pull request enters
  the queue. Confirm the queue is live afterwards: the repository's
  `mergeQueue(branch: "main")` in the GraphQL API is not null.
- [ ] Configure public repository presentation: description (`Local-first AI coding agent`), website (`https://paintedwolf.ai`), topics (`ai`, `agent`, `tauri`, `golang`, `solidjs`, `local-first`), and social preview image.

## DCO certification

[Original commit certification](../../.github/workflows/dco.yml) is the sole DCO publisher. The required `DCO-owned` commit status is the admission gate; the distinct `DCO audit` check holds full evidence and diagnostics and is not a required context. The action is pinned to a reviewed full commit SHA in `paintedwolf-ai/dco-checker`; policy and certification behavior belong there. This repository owns the event routing, least-privilege token scopes, and required-check configuration. The runner is Ubuntu 24.04. Every writer uses the repository-wide `dco-certification` concurrency group with `cancel-in-progress: false` and `queue: max`. Pending events queue in order instead of replacing another pull request’s pending certification.

GitHub Actions attribution identifies the publishing app, not the action's reviewed implementation. Review changes to the caller, action pin, permissions, and other workflows capable of writing commit statuses or checks as certification policy changes. A same-named result from another trusted workflow is within this trust boundary. The workflow never checks out contributor code or reads build artifacts with its write-capable token. Direct `pull_request_target` events initiated by Dependabot skip publication because GitHub restricts their token to read-only access; the `workflow_run` fallback remains enabled for those captured CI runs. Human events on bot-authored pull requests still certify.

### Adoption and upgrades

1. Select an immutable checker commit from a reviewed, qualified release. Review policy changes and the shared checker's hosted qualification evidence. Update the single action pin through a pull request; do not copy the checker into this repository.
2. Keep the migration or upgrade pull request draft while implementing it. Confirm parsed caller contracts cover triggers, CI identity, permissions, action pin, runner, and serialization. Mark it ready when complete; use hosted CI for the integration gate.
3. GitHub must register the workflow before it can be dispatched. For this initial migration, the caller is absent from the default branch, so merge its reviewed pull request through the existing required CI and DCO gates first. After the caller is available on `main`, dispatch certification from `main` for a ready pull request and confirm the `DCO-owned` commit status succeeds and its linked `DCO audit` reports the expected base/head, checker revision, policy version, exemptions, and source workflow run/attempt. Verify certification on a real merge group too. An obsolete event cannot certify a newer generation.
4. Require exactly the `check` check and `DCO-owned` commit status, both bound to GitHub Actions (integration ID `15368`), preserving all other ruleset protections and queue configuration. Remove the old `DCO` requirement and remove this repository from the external DCO App installation. Verify the resulting ruleset and app selection. Do not remove an installation shared by other repositories.

### Recovery and rollback

For transient API failures or an interrupted publication, dispatch **Original commit certification** from `main` with the ready pull request number. The checker resolves current evidence itself. Certification makes the required status pending before evaluating evidence and records a failure when evidence is unavailable. Inspect the linked `DCO audit` evidence identity and require the current `DCO-owned` status to succeed; do not rely on an old success for changed base/head evidence. Correct policy failures in the original commits as described in [Contributing](../../CONTRIBUTING.md#developer-certificate-of-origin).

If a checker release is defective, prepare a pull request returning the action pin to the last qualified immutable revision. Keep `DCO-owned` required and recertify affected ready pull requests after the rollback lands. If the required check prevents that repair from merging, the repository administrator must explicitly authorize a temporary protection change, record its exact scope, restore the requirement immediately after the repair, and verify certification. Never change check conclusions manually or broaden bypass actors as recovery.

The shared checker's maintainers own policy, implementation, releases, and qualification; this repository's maintainers own its caller pin and protections. Track unavailable evidence separately from a missing sign-off when investigating incidents.

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
   A schema change ships its registered migration step and recorded released
   baseline with the candidate ([compatibility](../compatibility.md)).
3. If provider integrations changed, run the [provider checks](#provider-integration-checks).
4. Mark the candidate ready and enable auto-merge; the merge queue runs the
   integration gate on the commit that lands. Wait for
   [Qualification](../../.github/workflows/qualification.yml) and
   [Build caches](../../.github/workflows/build-caches.yml) to pass on that
   commit; the release build restores the Go and Tauri compiles the warmer
   saved. One qualification runs at a time and a newer push replaces a pending
   one, so when main moves on first, tag the newest qualified commit that
   contains the candidate, after reviewing what else it brings.
   Tag that exact commit as `v<VERSION>` and push the tag. This starts
   [Release](../../.github/workflows/release.yml), which refuses a commit
   without a passing exact-commit qualification. A dependency-inventory refresh
   pushed to main bypasses the queue but is qualified like any other push.
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

Clients at 1.0.1 or earlier install updates through the Tauri updater plugin: they read the same `key-1` channel pointers, verify artifacts with the generation-1 artifact key, and require the signed `version` comment. They ignore the pointer's `withdrawn` marker and its rollout timing, so every 1.0.x client installs a new release at its next check, and only a restored last-good pointer stops one from installing a withdrawn release. From the first release with the native installer on, direct-download clients discover, stage, and install without visiting Settings. Candidate Linux and Windows entries remain unpublished until their activation adapters receive equivalent qualification.

Local tests establish state, migration, archive, and transaction invariants. A signed beta and clean VMs must establish the packaged lifecycle before promotion:

1. Install a released app A at a writable application location. Exercise chats, drafts, an editor, and multiple windows without opening update settings.
2. Offer signed beta B through the release channel; verify background staging and the update-ready notification and existing Settings restart action. Ordinary quit must stop the engine before exchange, install B, and remain closed. The next launch must run B and preserve released history and preferences.
3. Repeat with explicit restart, opt-out during download and after staging, channel change, a Homebrew receipt, a non-writable application directory, low disk space, and offline quit. Automatic work must never raise an administrator prompt or initiate restart.
4. Withdraw B after download. Its final offer check must prevent activation. Replace it with C and verify that C does not inherit B's staged identity.
5. Interrupt the helper before exchange, after exchange, and before the completion journal write; launch concurrently with activation. Both complete bundles must remain identifiable, and no process may start an engine from mixed installation files.
6. Exercise a supported released-store migration and its recovery snapshot. A failed new-version startup must retain recovery evidence, never downgrade the database or repeatedly relaunch.
7. Leave B's activation unconfirmed, then reinstall a different signed version at the same location, including A. The stale receipt must be archived, startup must work, and a later fix-forward update must remain available.
8. Run as a standard user with an unwritable application directory, from a mounted DMG, and under App Translocation. Observe multiple scheduler attempts: unsupported locations must not download repeatedly. After correcting the location, an explicit retry must reuse a valid cached archive and replace a corrupt one.
9. Start a second process from the same installed app while the first remains open. It must not hang awaiting an exclusive lifetime lease. Race a launch with helper activation and lease downgrade; no engine may start while an exchange is possible.
10. Download a quarantined, notarized beta through a browser. Verify that the copied helper runs directly from private staging with quarantine and signing attributes intact, including Gatekeeper assessment and executable-relative libraries. Exercise ordinary quit and explicit restart. Reuse of prepared files must survive a full signed-resource validation; tampering with resources or the main executable must prevent reuse.
11. Launch with a staged update while offline, and inject a helper-spawn failure. The first window must remain responsive, normal engine startup must resume, and Settings must retain the actual failure.
12. Measure cold and warm quit/restart timings on the complete signed beta bundle, including slower VM disks. Record preservation, parent verification, helper verification, exchange, and time to an interactive window. Verify that the preservation dialog disappears from eligibility after all windows acknowledge; cancel an already-open dialog during verification and confirm that update controls work again. Keep fresh helper integrity validation even when preparation just completed.
13. Preserve an unreadable or incompatible beta transaction journal. Confirm that startup identifies its exact path and that the documented archive-and-reinstall recovery works without removing user data.
14. Reopen A while the helper waits for another open instance. New launches must remain responsive. Race old and new processes through activation and startup confirmation; an old process must never open the new store. With another instance still open after 30 seconds, verify the committed update resumes on a later eligible launch.
15. Press Cmd+Q during explicit restart's offer check with automatic updates off. The restart must retain its exit request. Make one window never acknowledge: launch must defer after 30 seconds, and explicit quit/restart must keep offering cancellation after “Keep waiting.”
16. Run two installations at different paths, including a moved copy with a pending transaction at the old path. Staging, retry, cleanup, and activation must stay independent. Corrupt ready metadata must be quarantined without user file editing.
17. Toggle automatic updates during a manual download; it must finish. Toggle off during an automatic download; it must cancel. Switch channel during either; obsolete bytes must never become ready. Inject a helper-spawn failure and verify windows become usable before normal engine/credential recovery starts.
18. Offer the wrong signed architecture or a signed preview through stable; both must be refused before exchange. Check ordinary macOS installations without developer command-line tools.
19. Test a skipped signing-key bridge and a second restart into the successor release. Inspect version-bound signatures and package signing on both sides.
20. Serve the previous channel pointer and its signature again after a newer one was accepted; the client must refuse it. Serve a pointer with a signature made under the other channel's name, an unsigned pointer, and a pointer whose manifest version differs from its signed version; each must be refused without an offer. Withdraw a release through the halt workflow and confirm the replacement pointer carries a fresh feed signature.
21. Publish a manifest without `pub_date`; automatic checks must treat it as brand new (10% rollout). Corrupt `rejected.json` and `feed-state.json`; both must be quarantined without changing the automatic-update preference, and Settings must keep working.
22. Quit with a staged update while offline less than a day after the last successful check; it must install. Repeat with an offer last confirmed more than a day earlier; quit and launch must defer silently, and an explicit restart must report the failed check.
23. Keep a second instance open across the helper's ten-minute wait, then quit it; the committed update must install at that quit. Fill the disk before the helper's final journal write; the exchange must still relaunch the new version and Settings must report the unrecorded receipt.
24. Run on a proxy-only network and against an artifact URL that redirects; both must fail with a transport error and leave the installed version untouched.

Keep results with the release evidence. A local unit pass is not a claim of Gatekeeper, application-translocation, logout, power-loss, or cross-account filesystem behavior; these belong in the beta/VM qualification.

## If a release needs to be stopped

1. Run [Release halt](../../.github/workflows/release-halt.yml) with `bad_version`.
   Set `last_good_version` to restore an older release. While a generation-1
   feed is affected it is required: 1.0.x clients ignore `withdrawn` and would
   install the withdrawn release from a pointer that still names it, so the plan
   refuses to halt generation 1 without a replacement. Otherwise leave it empty to stop
   offering downloads when there is no safe replacement.
2. Review the default dry-run plan, then run it with `dry_run=false`. It updates
   all affected feeds and withdraws or replaces the tap and website downloads.
3. Verify the public download state. Existing installations are not downgraded.
   Publish a fixed version and explain the affected versions and recovery steps.

Halt preparation signs and validates every affected pointer before recording a permanent withdrawal or changing discovery. Dry runs perform the same signing checks. The workflow retains `prepared-halt/` with the reviewed plan (including its distribution scope), original pointers, exact prepared pointer/signature bytes and their SHA-256 digests. Apply validates the complete set and rechecks every feed before writing withdrawal markers, then uses those exact signed bytes. `application.json` records each attempted feed and its result; failures do not prevent attempts on the other prepared feeds. A retry accepts an unchanged original pointer or the already-applied replacement. A changed feed requires a new reviewed preparation. Pointer and signature objects are separate writes, so clients can briefly reject a mismatched pair; they must never install through that rejection.

For a retained preparation, run `./task release:halt -- --plan <halt-plan.json> --apply-prepared <prepared-halt-directory>` with publication credentials. Do not edit the signed files. Keep the generation feed keys available offline as part of recovery custody.

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
