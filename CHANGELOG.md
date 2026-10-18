# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

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
