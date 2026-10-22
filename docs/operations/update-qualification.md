# Update qualification

Automatic updates cross publication, cryptographic verification, filesystem
exchange, application restart, and data migration. Each boundary needs evidence
from the candidate that will ship. Passing one layer does not qualify the others.
The update implementation and operational controls are described in
[Updates](../dev-tasks.md#updates).

## Scope and evidence

Read `packaging/release-platforms.json` before choosing the test matrix. The
public automatic installation path is macOS Apple silicon. Linux packages use
package-manager updates and native discovery only. Windows remains a candidate
platform without a native activation adapter. Candidate artifacts do not establish
public updater support.

Record the source commit, product version, release build, artifact SHA-256,
signing generation, OS version, installation source, and result for every
packaged run. Retain update diagnostics and transaction receipts. Redact tokens
and credentials. A changed candidate requires evidence for its changed behavior;
an earlier source's receipt cannot qualify it.

| Layer | Existing verification | What it cannot establish |
|---|---|---|
| Native discovery and preparation | `./task den:test:rust`: signed feed fixtures, replay watermarks, archive bounds, architecture, cached artifact rejection, and contained links | CDN behavior and a real signed release download |
| macOS signature requirements | Native tests exercise `/usr/bin/codesign` against a disposable signed executable and reject an unrelated identity | Candidate bundle signing, notarization, or LaunchServices |
| Activation and recovery | Native tests exercise exchange, process leases, journal transitions, resource identity, and interrupted exchange reconciliation | Process death during a packaged update and its subsequent launch |
| Publication and withdrawal | `./task test:runner` includes the real pointer publisher against fixture storage, generation custody, exact signed bytes, and retry/withdrawal tests | Cloudflare credentials, CDN/cache policy, or real object visibility |
| R2 controls | `./task release:live-test` uses a unique `release-system-tests/` prefix, a disposable feed key, and cleanup | A production-trusting application installing an update |
| Store compatibility | `./task upgrade:rehearse` boots released engines and verifies migration, restart, retained semantics, and backup restoration | Desktop download, staging, quit, helper, or exchange |
| Candidate bundle | `./task bundle:verify -- --app <candidate.app> --require-signed` and `./task bundle:smoke -- --app <candidate.app>` | An update from a previous installed release |

Use the repository's verification admission and receipt procedures. Provision
isolated checkouts with `./task setup-dev -- --frontend` and
`./task setup-dev -- --shell-resources` before native tests. Full exact-commit
release qualification remains required independently of these focused checks.

## Packaged update rehearsal

Use a disposable machine or a dedicated clone of a clean VM. Preserve existing
VMs, running applications, and real user stores. Restrict test storage credentials
to the rehearsal bucket. Test feeds must use a deliberately built test application
with its matching trust registry; production clients must retain their embedded
origin and keys.

Start with the last broadly distributed release and each supported RC as the
installed source. Use the actual signed, notarized candidate as the destination.
Retain a representative history fixture and its semantic expectations. Verify
the following cases before broad activation:

| Scenario | Required observation |
|---|---|
| Background update | Discovery occurs without opening Settings; only eligible automatic checks stage the candidate; the app remains usable |
| Normal quit and explicit restart | Work saves, the engine stops, and one helper exchanges the bundle; restart intent is honored without an unsolicited restart on ordinary quit |
| Next launch | A committed handoff resumes safely and the installed version starts its own engine |
| Startup confirmation | The complete installed bundle matches the preparation receipt and passes publisher verification; healthy startup precedes removal of the previous bundle |
| Two running copies | A second process delays exchange; releasing the last running copy permits activation; waiting helpers do not restore revoked restart intent |
| Interrupted transfer | Disconnect and restart during download; partial files never become ready; retry eventually downloads and verifies the complete archive |
| Rejected content | Alter the pointer signature, artifact bytes, prepared resource, or installed resource in the disposable environment; rejection never confirms success or discards the retained previous bundle at confirmation |
| Feed changes | Withdrawal, replay, a changed candidate, channel changes, and mismatched pointer/signature publication never authorize the rejected candidate |
| Offline quit | A confirmed offer within 24 hours may install; expired, future-dated, or revoked confirmation cannot authorize offline activation |
| Interrupted activation | Stop only the owned process at prepared, committed, exchanged-but-unrecorded, and activated-but-unconfirmed boundaries; reopen and require journal/installed-byte agreement without exchanging the old bundle back |
| Installation restrictions | Read-only volume, unwritable parent, insufficient VM disk space, and Homebrew installation produce the declared blocked/retry behavior |
| Preferences and data | Disabled automatic updates remain disabled; channel/source preferences and credentials survive; migrated history remains usable after a second launch and explicit restore |
| Fix-forward recovery | A halt stops discovery without downgrading installed clients; a higher signed recovery version preserves the upgraded store |

Keep the previous bundle while confirmation fails. Recovery must not launch an
older binary against a migrated database. A halt prevents further distribution;
it does not repair clients that already installed the withdrawn release.

An explicit update restart must start a new instance with `open -n`, both from
the helper and when startup recovery exchanges the application beneath its
launching process. Releasing the installation lease does not guarantee that
LaunchServices has retired the old process. Plain `open` can report success by
reopening that process without launching its replacement. A helper that times
out waiting for a still-running copy only activates that existing copy.

The updater shipped in 1.0.2-rc1 and 1.0.2-rc3 passes its inline `codesign`
requirement without the required `=` prefix. Those clients cannot prepare an
otherwise valid candidate through that path. The correction in 1.0.2-rc4
([#920](https://github.com/paintedwolf-ai/paintedwolf-code/pull/920)) cannot change
code already executing in an older installation. Verify refusal and preservation
of the old app for these sources, then manually install a corrected signed build
before testing a successful automatic transition. Keep that bootstrap result
separate from a successful update; never bypass signature verification to make
the affected RCs update.

## Release decision

The source-level tests, signed candidate audit, R2 controls, packaged transition,
and exact-source release qualification must all have explicit results. Treat
missing evidence as unverified. Do not infer updater success from a healthy
manually installed candidate, a successful migration rehearsal, or a published
feed alone.

## Nix owner and recovery qualification

[Nix consumer instructions](../dev-tasks.md#nix-installations) separate the running package, upstream feed, selected source, installation owner and pin. Source availability remains `not_checked`; neither a store path nor a user-profile target establishes ownership. Signed cache configuration uses its own published Nix key and stays optional.

For each supported CPU architecture, exercise an exact released source with the product cache absent and with explicitly configured signed substitution. Record the source commit/lock and installed output. Independently use a NixOS configuration, Home Manager configuration, chosen user profile and temporary output link; also launch an output without owner metadata and preserve the unknown state. Test an intentional old pin, a missing source, stable/preview offers, rejected or stale feed metadata and release withdrawal. Update checks must not change the owning configuration, pin, store, profile or running process.

For each actual owner, switch N → N+1 through that owner's tools, preserve a running generation until exit and relaunch the selected output. NixOS custody must refuse unselected/stale generations and modified images, manifests or executable mappings; intentional overlap lists exact generations. Disable/re-enable and rollback must retain service state and credentials. Missing service/agent cases retain the documented password or recovery path.

Capture a recovery snapshot before a live schema migration. A package rollback against a newer/unknown database must refuse mutation; explicitly restore the chosen compatible snapshot and verify retained history and device credentials. Package generation history never substitutes for data recovery. Cache tampering, wrong signatures, key rotation/withdrawal and cold substitution are #945's cache checks. Package construction is #927, generation layout #928, system integration #929 and installed qualification #932/#457. These remaining checks are not implied by focused native or UI tests.
