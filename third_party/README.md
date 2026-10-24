# Vendored dependencies

## Parser dependency

`gotreesitter/` contains the complete Go module source for
`github.com/odvcencio/gotreesitter` v0.52.0, commit
`2295871057f860598a006d6068588a6303fefb02`.

Upstream module checksum: `h1:/CpWH3r0A7BbsM0jiKVn3s0/ZrzoOYZvXC0Kd/4Meww=`.
The upstream license and source notices remain in the module directory.

The upstream Git ignore files are omitted so all module files remain visible
to this repository.

The source modifications are recorded in [gotreesitter.patch](gotreesitter.patch).
Swift recovery must inspect erroneous `if_statement` and `while_statement`
parents even when those statement nodes exist. Otherwise, an optional-binding
condition can absorb its body as a trailing closure and prevent recovery of the
complete source. The existing recovery pass reparses with synthetic parentheses
and remaps the tree to the original source coordinates.

The PowerShell grammar requires a statement in its program production, rejecting
valid scripts containing only comments or whitespace. The local compatibility
pass supplies that empty-program production only when the existing tree proves
every non-whitespace byte belongs to a clean comment. Missing nodes, executable
tokens, unterminated comments, and uncovered non-whitespace bytes retain their
errors. Regression cases live in
[`comments_test.go`](../lycaon/internal/scan/sourceview/comments_test.go).

TypeScript and TSX finalization stays inside the active parse whenever a timeout
or cancellation flag is set. The upstream lazy finalizer creates a new parser
without those limits, so deferring it would escape the caller's budget.

The host regression fixtures are in
[`swift_test.go`](../lycaon/internal/syntaxhealth/swift_test.go) and its adjacent
`testdata/swift/` directory. They retain the original source bytes.

To update this dependency, replace the module directory with the verified source
of the selected release and review whether the patch is still needed. Update
`lycaon/go.mod`, its module checksums, and this record together. Run the source
parser regression suites and the repository closeout checks through `./task`.
When an upstream release includes the correction, remove the local replacement
and copy after those checks pass.

## MLX build inputs

`mlx/` holds checksummed MLX C++ dependency archives. The complete locked
`mlx-sys` 0.6.0 crate source (about 1 MiB) lives in the decision engine workspace
at `lycaon/internal/decide/native/vendor/mlx-sys`, keeping Cargo dependency
identity relative to that workspace. Its original crates.io archive SHA-256
is `1848b6f1686114039e6e0adcf1578911e2e4d7707b9768ad61da074132319e1c`,
matching the pre-replacement Cargo lock entry. Source commit and archive pins are
recorded in `mlx/pin.json`; upstream source notices and both Rust crate licences
remain in its directory.

`mlx/mlx-sys.patch` records the build-script change: Cargo tracks repository
inputs, and CMake source/toolchain files live in the crate's persistent output
directory. Unchanged files retain their timestamps; stale entries are removed
only inside that owned staging tree. The shared CMake policy verifies the
current archives and version before FetchContent can run. No crate API or
version changes. Update the crate copy, patch, pin, notices and its Cargo lockfile
together; verify offline builds, fresh-snapshot object reuse and warmed-cache
mismatched-pin refusal through the existing task targets.
