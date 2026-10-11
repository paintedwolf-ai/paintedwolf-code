# Dependencies

When the Go module may take a third-party library, and the constraints that ride on particular ones. The [dependency inventory](operations/dependency-inventory.md) highlights update priorities and constraints, with links to every pin and its update policy; its source is [`dependencies/`](../dependencies/README.md).

**See also:** [Licensing](licensing.md) · [Security](security.md) · [Dependency inventory](operations/dependency-inventory.md) · [Docs map](README.md)

## Constrained dependencies

These carry a rule beyond "we use it":

| Package | Constraint |
|---------|------------|
| [`github.com/getkin/kin-openapi`](https://github.com/getkin/kin-openapi) | **Test-only.** Imported from `lycaon/test/openapi`, `test/security`, `test/contract`, never `internal/`. Production `ServeHTTP` does not validate against OpenAPI at runtime |
| `modernc.org/sqlite` | The single repo-wide driver: pure Go, FTS5 built in, no CGO or build tag. Schema SSOT is `schema.sql` ([SQL persistence](sql-persistence.md)) |
| `github.com/tiktoken-go/tokenizer` | Embedded vocabularies behind `internal/tokenest`; no runtime network or Python dependency. Only explicitly declared model encodings use these counts; unknown models use the labeled estimate |
| [sqlc](https://sqlc.dev) | Codegen CLI, not a runtime dependency (`./task db:sqlc`). Typed queries from `schema.sql` plus hand-written `.sql`; no ORM |
| [`github.com/flosch/pongo2/v6`](https://github.com/flosch/pongo2) | The one template engine. `internal/pongoplain` defines the bounded dialect and execution policy; `internal/prompts` implements static include composition and layered loading |
| [`filippo.io/age`](https://pkg.go.dev/filippo.io/age) · [`github.com/keybase/go-keychain`](https://github.com/keybase/go-keychain) | Age-encrypted credential vaults in `internal/credentialstore`; macOS Security.framework stores only the device-local vault identity |
| [`@tanstack/solid-virtual`](https://tanstack.com/virtual/latest/docs/framework/solid/solid-virtual) | Transcript DOM windowing with measured variable rows. The transcript motion controller owns tail anchoring and applies `shiftContent` corrections |

## Review checklist (new dependency)

1. Does it replace more than ~50 lines of subtle infrastructure code?
2. Is it maintained and widely used in its ecosystem?
3. Can it be wrapped behind an existing interface (`Provider`, `OutputParser`, …)?
4. Does it increase attack surface for the local sidecar (network parsers, eval engines)?
5. **Is something in the tree already doing this job?** There is one expression engine, one template engine, one SQL driver, one registry generic, one in-process event bus, and one token estimator. Extend the existing one or replace it outright; never run two.

If (4) is yes and (1) is no, hand-roll it.

## Bundled git toolchain

The app ships a **pinned** git + git-lfs binary tree rather than calling whatever `git` is on the host `PATH`. Pin and fetch are part of the build; the extracted tree is not committed.

| Item | Detail |
|------|--------|
| **Artifacts** | `git`, `git-lfs`, and a slim `libexec/git-core/` from [desktop/dugite-native](https://github.com/desktop/dugite-native) release tarballs |
| **Pin** | `lycaon/config/gitengine/pin.yaml`: `git_version`, `lfs_version`, and each platform's URL template, build id, exact reported version, and SHA-256 |
| **Fetch** | `./task gitengine:fetch` (`scripts/gitengine-fetch.sh`) downloads, verifies the hash, extracts into `lycaon-den/src-tauri/engine-root/gitengine/`, then prunes weight. A SHA-256 mismatch deletes the download and **fails the fetch** |
| **Prune** | Unix drops Git Credential Manager, server and legacy helpers (`git-daemon`, `git-shell`, `git-http-backend`, `git-imap-send`, …), and dugite's `git-* → git` multi-call aliases: the host always runs `bin/git <subcommand>` with `GIT_EXEC_PATH` set, so those aliases are unused, and Tauri's resource bundler would otherwise dereference each symlink into a full copy of `git`. `git-remote-https` is a copy of `git-remote-http`; ftp/ftps helpers are removed. Windows keeps the `cmd` / `mingw64` / `usr` runtime topology and removes documentation and credential-manager entry points. Budgets: 40 MB Unix, 140 MB Windows |
| **Resolution** | `LYCAON_GIT_BINARY` (honoured only when `LYCAON_TEST` is set), else the launcher-provided `LYCAON_ENGINE_ROOT`, else unavailable. No `PATH` lookup and no system-git fallback; preflight reports `GIT_ENGINE_UNAVAILABLE`. `scripts/gitengine-ensure.sh` stages the tree; `./task setup-dev`, `./task den:sidecar`, and the e2e harness provision it, so an offline host still gets a sidecar |
| **Test binaries** | A test binary has no engine beside it. Packages whose tests touch git call `gitengine.TestingEnableBundledBinary()` from a `TestMain`; it points `LYCAON_GIT_BINARY` at the checkout's staged tree and sets `LYCAON_TEST=1` |
| **Packaging** | The tree is mapped under Tauri `bundle.resources` as `engine-root/`; it is **gitignored** and produced by the fetch/build step. `./task bundle:verify` (`internal/bundleverify`) rejects a shipped tree over the size budget or that still contains a materialized `git-status` alias |
| **Signing** | Every Mach-O in the macOS tree is signed, not just `bin/git`: git executes its own helpers out of `libexec/git-core/`, and notarization rejects any nested binary without a Developer ID signature, hardened runtime, and secure timestamp. A Windows build, a candidate platform that is not published, signs the Git and Git LFS entry points through Azure Artifact Signing before packaging. `scripts/den-build-bundle.sh` refuses to bundle when the platform tree is missing |

### Bump procedure

1. Edit `lycaon/config/gitengine/pin.yaml` (versions, URL/build, and the new SHA-256).
2. Run `./task gitengine:fetch` and confirm it exits zero.
3. Run the git parity suite against a reference git (`LYCAON_GIT_REFERENCE` set; `lycaon/test/integration/git_parity_test.go`). It covers large-file storage driven from a path containing a space, which is what every installed path looks like.
4. Commit the pin manifest. Do **not** commit the extracted `engine-root/gitengine/` tree.

When a CVE lands in git or git-lfs, bump the pin to a fixed dugite-native release and repeat.

## Managed headless browser

Visual tools (capture, rasterize, measure, filmstrip, page sessions) run a **pinned** `chrome-headless-shell` from Chrome for Testing, with the same split as the git toolchain: [`browserengine`](../lycaon/internal/browserengine) resolves and provisions the pinned binary, [`browser`](../lycaon/internal/browser) launches and pools it. Hermetic-or-fail, never a browser the user installed.

| Item | Detail |
|------|--------|
| **Artifacts** | `chrome-headless-shell` plus its sibling runtime (`icudtl.dat`, `*.pak`, SwiftShader): a flat Chrome-for-Testing tree |
| **Pin** | [`chrome_pin.go`](../lycaon/internal/browserengine/chrome_pin.go): version plus a per-platform SHA-256. A missing pin for the host platform is as fatal as a mismatch |
| **Fetch** | `./task browser:ensure` provisions the development channel's managed cache (`browser-cache` under `~/.config/paintedwolf-dev`); `scripts/stage-engine.sh` (full mode) and the bundle build stage the same tree into `engine-root/browser/` |
| **Resolution** | `LYCAON_BROWSER_BIN` → bundle (beside the engine executable, or the `.app`'s `Resources/`) → managed cache. **No `PATH` lookup and no system-browser fallback**; a miss is `BROWSER_ENGINE_UNAVAILABLE`, which preflight surfaces. Every source must be a *complete* install with its resources beside the executable, so the override can name another hermetic tree but never an installed browser's launcher stub |
| **Why not an installed browser** | It is unpinned, so a capture is not reproducible across machines; a macOS app bundle relaunches itself out of the process handle the host holds, so the host can neither confine nor stop it; and it does not survive the Seatbelt profile and reduced environment `LaunchHeadless` uses |
| **Process shape** | Killing the browser is enough: its renderer, GPU, and network children exit when the browser's IPC channel breaks. So `launchConfined` leaves chrome in the launching process's group, unlike [`mcp`](../lycaon/internal/mcp/spawn_unix.go), which gives a stdio server its own group because that tree can outlive its parent. Shutdown closes the pool as a tracked resource (`runtime_resources.go`, order 60). A `SIGKILL`ed sidecar has no parent-death primitive outside Linux, so the browser is registered with the engine's [process reaper](../lycaon/internal/exec/reaper_unix.go) |
| **Test binaries** | Packages whose tests launch a browser call `browserengine.TestingEnableBundledBinary()` from a `TestMain`, which points the resolver at the checkout's staged `engine-root/browser`, or else at the browser `browser:ensure` installed under the developer's own home, which isolation exports as `PW_TEST_HOST_HOME` before replacing `HOME`. Gate every such test on `browsertest.SkipIfNoBrowser`, which probes exactly what a launch resolves |
| **Not silently uncovered** | `LYCAON_BROWSER_INTEGRATION=1` provisions over the network; `LYCAON_BROWSER_REQUIRED=1` turns the skip into a failure. CI's `confinement` job sets the latter for its browser step, because a job that provisioned a browser and then reports "no browser, skipping" is green while covering nothing |

## Decision model

Bialy, the host's tuned local decision engine (`bialy`, [`lycaon/internal/decide/native`](../lycaon/internal/decide/native)) serves a pinned Laya checkpoint natively. The packaged app bundles the checkpoint's weights (about 680 MB); a development run provisions them at first launch.

| Item | Detail |
|------|--------|
| **Artifacts** | A Laya checkpoint directory (`rl_agent_config.json`, `model.safetensors`, `encoder/config.json`, `tokenizer/`) from the Hugging Face Hub, plus the trained head files staged under `engine-root/decide/heads/` |
| **Pin** | [`models.go`](../lycaon/internal/decide/bialy/models.go): hub id, commit revision, and a SHA-256 per file. `ShippedModel` is the checkpoint the bundled heads fit |
| **Fetch** | [`provision.go`](../lycaon/internal/decide/bialy/provision.go) downloads each file to a sibling temporary path, verifies its digest, and renames it into `decide-models/<id>@<revision>/` under the device configuration; a `.complete` marker is written last. `pw decide ensure` runs it by hand, and `scripts/stage-engine.sh` runs it on the build machine and bundles the result, marker included, as `engine-root/decide/models/<id>@<revision>/` (`bundleverify` requires every pinned file at its pinned size). The engine's boot path runs it in the background only when no bundled checkpoint resolves, outside a packaged app. Egress class `decide_model_download`, endpoint `https://huggingface.co` |
| **Heads** | [`lycaon/config/packs/painted-wolf/platform/host/decision-release.json`](../lycaon/config/packs/painted-wolf/platform/host/decision-release.json) identifies one complete release: required `turn-load`, `unit-rank`, and `code-rank` heads, with optional `web-rank`. Staging verifies every SHA-256, label and backbone before replacing the staged directory and writes `release.json` beside the heads. `BIALY_HEADS_DIR` selects the artifact directory; it does not bypass verification. An isolated candidate checkout updates this same manifest, so the host and staged weights cannot select different releases. Missing or mismatched heads fail the build rather than substituting the checkpoint head. Restore artifacts from the release archive described in the factory training recipe. Heads are open weights under Apache-2.0; the engine also refuses a head trained over another backbone. |
| **Resolution** | `LYCAON_DECIDE_BINARY` → `bialy` beside the host executable (`Contents/MacOS/` in the app bundle); `LYCAON_DECIDE_MODEL_DIR` → the bundled checkpoint under the engine root when it is complete → the managed checkpoint. A miss is the `decision_engine` preflight probe's `DECISION_ENGINE_UNAVAILABLE`; every ranking site then keeps its lexical order and every turn decision falls back |
| **Runtime** | candle (Rust) on every platform; on Apple silicon the same model runs on Apple's [MLX](https://github.com/ml-explore/mlx) through `mlx-rs` (`--device mlx`, the `auto` choice there), which the build compiles from source with CMake and whose GPU kernels ship as `engine-root/decide/mlx.metallib`, handed to the engine as `--metallib` (`LYCAON_DECIDE_METALLIB` overrides) |
| **Build** | `./task build:decide` (`scripts/decide-features.sh` picks the features: `metal,mlx` on Apple silicon, `BIALY_FEATURES=cuda` elsewhere with a CUDA toolchain), `./task decide:test`; `scripts/stage-engine.sh` and `scripts/build-dev-engine.sh` build it with the other sidecars. The first MLX build compiles the framework and takes several minutes |

## Licenses

| Artifact | License |
|----------|---------|
| **Painted Wolf Code application** | [Apache-2.0](../LICENSE); see [Licensing](licensing.md) |
| **Painted Wolf Code rules** | [CC-BY 4.0](licensing.md#painted-wolf-code-rules-cc-by-40) |
| **Third-party dependencies** | `./task licenses:notices` generates the notices from the dependency manifests and the bundled-binary catalog |
