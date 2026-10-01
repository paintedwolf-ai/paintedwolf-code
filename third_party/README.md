# Parser dependency

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
