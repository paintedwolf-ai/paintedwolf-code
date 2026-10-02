# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

## [1.0.0]

First stable release of **Painted Wolf Code**, a local-first AI coding agent
for macOS on Apple silicon.

### Changed

- Your stored secrets are unlocked per chat. Approving a recipient and
  unlocking are separate: one Touch ID confirmation unlocks a chat, and it
  locks again after 15 idle minutes, after 4 hours, when you lock it from the
  composer, or when you step away from the computer.
- An approval level in an `approvals.yaml` that the app does not recognize is
  no longer read as Balanced. On this device it stops startup with a message
  naming the file; in a project it applies Strict, and project approval
  settings list each part of the file that was not applied.

### Fixed

- Updating from an earlier preview no longer fails with a message that the
  update was signed for a different version.
- A failed update check reports a verification problem instead of telling you
  to check your connection.
- Release notes in Settings → Updates show formatted headings and lists.
- Edits to a project's `approvals.yaml` apply right away, including after
  changing project approval settings in the app.
- The stable and preview Homebrew casks no longer install over each other.

## [1.0.0-rc.3]

### Changed

- Managed secrets act as a vault: storing or reading a secret asks for your
  presence (Touch ID) wherever the request starts, including file writes and
  agent requests, not only in project configuration.
- The document editor's core runs as a native program alongside the engine.

### Fixed

- Opening or editing documents no longer crashes the engine in signed release
  builds.
- The app restarts its engine after an unexpected exit and says when it stops.

## [1.0.0-rc.2]

### Fixed

- The credential vault opens on macOS. Release builds rejected the identity
  they had just stored in the Keychain, so 1.0.0-rc.1 could not start.
- Reads outside the attached folders ask at every approval posture.
- A refused write outside the write roots offers the enclosing repository.
- Untagged report fences in a closing answer are read as report fields.

## [1.0.0-rc.1]

### Added

- First public release candidate of **Painted Wolf Code**.
- Sessions, tools, workflows, approvals, and mediated sandboxing managed by the
  local host — Apache-2.0 application code; see [`LICENSE`](./LICENSE) and
  [`docs/licensing.md`](docs/licensing.md).
