# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

## [1.0.3-rc1]

First release candidate for 1.0.3, published on the preview channel to
distrust the compromised eMudhra intermediate certificates and to exercise the
signed desktop self-upgrade release gate.

### Security

- The engine and desktop app no longer trust the eight eMudhra intermediate
  certificates whose private keys are recoverable, on Go 1.26.10,
  rustls-webpki 0.103.16, and webpki-roots 1.0.10. On Linux, bundled Git
  relies on the system's `ca-certificates` update for the same protection.

## [1.0.2]

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

- Moving large folders updates their directory identity without rewriting every
  descendant's history. File operation completion reaches Files immediately.
- New Move to Trash operations use native Trash recovery without first copying
  or hashing the tree. Undo requires the item to remain in Trash; existing
  retained recovery history keeps its previous guarantees.
- Undoing a new create or duplicate preserves the actual output, including later
  edits, through native Trash. Copies retain no redundant full-tree backup.
- Source history upgrades transactionally to directory identities, preserving
  saved pins and retained content while removing redundant checkpoint copies.
- Source mutation receipts show accepted changes, and recorded checks show
  whether they match current source.
- A single file-watch stream is shared across watched roots.
- The previous “check for updates” preference now enables automatic checks and
  silent downloads, with verified installation on quit or restart. Turning
  automatic updates off keeps manual update actions available, and Homebrew
  installations present a notification-only update notice and settings summary
  when a newer package is available.
- Workflow runs pin sealed definitions and guidance. Project guidance overlays
  do not change a sealed run; retained shipped workflow versions remain available.
- Coordinator and execution-state guidance surface current checklist readiness,
  completed document-read prerequisites, workspace tools, user-visible updates,
  and observed tool outcomes more clearly without naming unoffered or deferred
  tools.
- Tool feedback presents exact source text and invocation outcomes more clearly,
  and tool rejection and syntax recovery preserve bounded host diagnostics,
  required-argument observations, and source-read prerequisites.
- Missing executable feedback names available catalog equivalents.
- Compacted history is enriched asynchronously without blocking chat and scales
  its representations to the receiving context.
- Transcript scrolling sizes its runway to the full session bounds and seeks
  unloaded history on demand.
- Notice cards use simpler presentation and clearer action spacing.
- Power capability facts stay current while settings are open.

### Fixed

- Unoffered, unparsed, and provider-dropped tool calls recover in-turn, with
  parser grammars declared in the provider catalog.
- Local OpenAI-compatible providers preserve reasoning content across multi-step
  tool loops, omit tool definitions on non-callable turns, fold post-tool system
  guidance into the trailing tool result, and recover XML `<function=...>` tool
  calls and `<tool_call>` blocks after prose.
- Internal step notes mark model working notes as data while keeping host
  directives authoritative and stripping parroted markers from replies.
- Thinking controls respect the selected model's capabilities.
- Terminal empty responses recover with bounded feedback.
- Provider request budgets match native dispatch, and complete provider frames
  fit within receiver context.
- Fresh user direction renews approval review.
- Transcript history keeps scroll position and measured row heights across
  paging, resizing, and restore; estimates no longer count hidden attachment
  content such as extracted PDF text.
- Session history recovers through bounded recall pages, and recorded tool
  results stay discoverable by their target path.
- Hidden answers remain visible across `wait` transitions, terminal screen
  results preserve exit status, and repository summaries refresh when the index
  changes.
- Browser static captures validate entry files, page approvals resolve mounted
  directories, and scratch file discovery refreshes after writes.

## [1.0.1]

### Fixed

- Commands marked as verification now follow the session's network posture and
  approvals. They no longer lose all outbound network access, which blocked
  live checks against remote services with no way to approve them.

## [1.0.0]

Painted Wolf Code 1.0 is here: our first stable release.
