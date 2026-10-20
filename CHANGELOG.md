# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

## [1.0.2-rc8]

Eighth release candidate for 1.0.2, published on the preview channel for
in-app update testing from RC7 and incorporating Homebrew update notification,
local model provider, and coordinator prompt fixes.

### Changed

- Homebrew installations present a notification-only update notice and settings
  summary when a newer package is available.
- Coordinator progress and tool-loading guidance describe operations and targets
  without naming unoffered or deferred tools.

### Fixed

- Local OpenAI-compatible providers preserve reasoning content across multi-step
  tool loops, omit tool definitions on non-callable turns, fold post-tool system
  guidance into the trailing tool result, and recover XML `<function=...>` tool
  calls and `<tool_call>` blocks after prose.
- Internal step notes mark model working notes as data while keeping host
  directives authoritative and stripping parroted markers from replies.

## [1.0.2-rc7]

Seventh release candidate for 1.0.2, published on the preview channel for
in-app update testing from RC6 and incorporating coordinator, tool-recovery,
and release-verification fixes.

### Changed

- Coordinator and execution-state guidance surface current checklist readiness,
  workspace tools, user-visible updates, and observed tool outcomes more clearly.
- Tool rejection and syntax recovery preserve bounded host diagnostics,
  required-argument observations, and source-read prerequisites.

### Fixed

- macOS release staging validates stapled notarization tickets on both the
  application bundle and disk image and retains native ticket diagnostics.
- RPM package staging accepts domain vocabulary (`Agentic`, `toolchain`) during
  `rpmlint` verification.
- Hidden answers remain visible across `wait` transitions, terminal screen
  results preserve exit status, and repository summaries refresh when the index
  changes.
- Browser static captures validate entry files, page approvals resolve mounted
  directories, and scratch file discovery refreshes after writes.

## [1.0.2-rc6]

Sixth release candidate for 1.0.2, published on the preview channel for
in-app update testing from RC5.

### Fixed

- Restarting after an update starts a fresh application process, including
  recovery when a launch encounters the previous version during replacement.
- Update startup confirmation verifies the complete installed application and
  its publisher signature before removing the previous application bundle.
- Update failure recovery checks the complete candidate bundle when recording
  whether activation succeeded.

## [1.0.2-rc5]

Fifth release candidate for 1.0.2, published on the preview channel to verify
in-app upgrades from 1.0.2-rc4 and complete Linux candidate package verification.

### Fixed

- Debian package assembly records `/etc` paths in `DEBIAN/conffiles` and omits
  them from `DEBIAN/md5sums` so `lintian` verification succeeds during release
  staging.

## [1.0.2-rc4]

Fourth release candidate for 1.0.2, published on the preview channel for upgrade
testing. Replaces 1.0.2-rc1 and 1.0.2-rc3 on preview.

### Fixed

- macOS in-app update verification passes its code-signing requirement as an
  inline `codesign` expression rather than a file path, so staged updates verify
  and install cleanly. Existing 1.0.2-rc1 and 1.0.2-rc3 preview installations
  must upgrade once via the disk image or Homebrew cask.

## [1.0.2-rc3]

Third release candidate for 1.0.2, prepared for preview upgrade testing.
Includes the changes listed under 1.0.2-rc2, whose publication did not complete.

### Added

- Linux desktop integration: opening, revealing, trashing, and watching files
  goes through the desktop services, notifications arrive through the session
  bus, clipboard runs through the GTK display connection, and window chrome
  follows GNOME conventions.
- Linux commands run confined with namespaces, Landlock, and seccomp, with
  desktop proxies, session providers, Git host services, and SSH agents
  resolved from the native session.
- Linux builds bundle a Tesseract OCR engine and OpenGrep engines for x86_64
  and aarch64.
- The rerank engine runs on any Vulkan GPU when Metal is unavailable.

### Changed

- Tool feedback presents exact source text and invocation outcomes more clearly.
- Missing executable feedback names available catalog equivalents.
- Coordinator guidance handles checklist instructions and completed document-read
  prerequisites consistently.
- Compacted history is enriched asynchronously without blocking chat and scales
  its representations to the receiving context.
- Transcript scrolling sizes its runway to the full session bounds and seeks
  unloaded history on demand.
- A single file-watch stream is shared across watched roots.
- Power capability facts stay current while settings are open.
- Source mutation receipts show accepted changes, and recorded checks show
  whether they match current source.

### Fixed

- Release verification retains native notarization ticket validation errors to
  diagnose signing failures without weakening release checks.
- Unoffered, unparsed, and provider-dropped tool calls recover in-turn, with
  parser grammars declared in the provider catalog.
- Terminal empty responses recover with bounded feedback.
- Provider request budgets match native dispatch, and complete provider frames
  fit within receiver context.
- Session history recovers through bounded recall pages, and recorded tool
  results stay discoverable by their target path.
- Event stream bursts end promptly so WebKitGTK delivers them.

## [1.0.2-rc2]

Second release candidate for 1.0.2. Publication stopped at notarization ticket
validation; this candidate was not offered on the preview channel.

### Changed

- Structure diagnostics report directory character declarations and cohesion evidence.
- Notice cards use simpler presentation and clearer action spacing.

### Fixed

- Thinking controls respect the selected model's capabilities.
- Fresh user direction renews approval review.
- Transcript history keeps scroll position and measured row heights across
  paging, resizing, and restore; estimates no longer count hidden attachment
  content such as extracted PDF text.

## [1.0.2-rc1]

First release candidate for 1.0.2, published on the preview channel for upgrade testing.

### Changed

- Moving large folders updates their directory identity without rewriting every
  descendant's history. File operation completion reaches Files immediately.
- New Move to Trash operations use native Trash recovery without first copying
  or hashing the tree. Undo requires the item to remain in Trash; existing
  retained recovery history keeps its previous guarantees.
- Undoing a new create or duplicate preserves the actual output, including later
  edits, through native Trash. Copies retain no redundant full-tree backup.
- Source history upgrades transactionally to directory identities, preserving
  saved pins and retained content while removing redundant checkpoint copies.

- The previous “check for updates” preference now enables automatic checks and
  silent downloads, with installation on quit. Turning automatic updates off
  keeps manual update actions available.
- Workflow runs pin sealed definitions and guidance. Project guidance overlays
  do not change a sealed run; retained shipped workflow versions remain available.

## [1.0.1]

### Fixed

- Commands marked as verification now follow the session's network posture and
  approvals. They no longer lose all outbound network access, which blocked
  live checks against remote services with no way to approve them.

## [1.0.0]

Painted Wolf Code 1.0 is here: our first stable release.
