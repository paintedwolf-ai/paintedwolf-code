# Release manifest

## Release identity

Resolve this record before building; every embedded version and generated manifest derives from it.

```text
commit:   9f2c1ab   version: 2.4.0   tag (unpushed): v2.4.0
config:   release    toolchain: go 1.26.6, lockfile clean
targets:  darwin/arm64, linux/amd64, linux/arm64
```

## Artifact inventory

One row per artifact. State what each proves; leave a column empty rather than claiming evidence you do not have.

| Target | File | Size | SHA-256 | SBOM | Signature / notarization | Provenance |
|---|---|---|---|---|---|---|
| darwin/arm64 | app-2.4.0-darwin-arm64.tar.gz | 41.2 MB | `3f1c…` | yes | signed, notarized | attestation `…` |

Compute digests over the exact files you hand off (`sha256sum <file>` in `command`). Rebuilding an artifact gives it a new identity and new rows.

## Handoff

- Release identity, as above.
- Gates run, with the exact commands and their results.
- Artifact inventory.
- Clean-install evidence: where the distributable was unpacked or installed, and what was exercised (startup, version output, upgrade or migration, the shortest critical user path).
- Known limitations.
- Remaining human actions, each named individually: tag push, package or image publish, updater metadata upload, announcement, production rollout.
