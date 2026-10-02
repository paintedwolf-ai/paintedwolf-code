# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

## [Unreleased]

### Fixed

- Commands marked as verification now follow the session's network posture and
  approvals. They no longer lose all outbound network access, which blocked
  live checks against remote services with no way to approve them.

## [1.0.0]

Painted Wolf Code 1.0 is here: our first stable release.
