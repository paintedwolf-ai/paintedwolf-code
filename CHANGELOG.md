# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

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
