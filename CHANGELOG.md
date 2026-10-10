# Changelog

All notable changes to Painted Wolf Code are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for the host/desktop stream (`VERSION` + `v*` git tags). See
[`docs/dev-tasks.md`](docs/dev-tasks.md) § Release & versioning.

## [1.0.2]

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

## [1.0.1]

### Fixed

- Commands marked as verification now follow the session's network posture and
  approvals. They no longer lose all outbound network access, which blocked
  live checks against remote services with no way to approve them.

## [1.0.0]

Painted Wolf Code 1.0 is here: our first stable release.
