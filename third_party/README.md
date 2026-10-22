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


## Portable command parser

`mvdan-sh/` contains the complete Go module source for `mvdan.cc/sh/v3`
v3.14.1, commit `a3f0c75d21d918756fa38de8b5d3429efde7948b`.
The release archive was verified against its Go module checksum before extraction:
`h1:bXkhQWNHCs0KZEChF8hYS6FC+T2N9mUZLbQv9blditI=`.
The module metadata checksum is
`h1:syYCoFET8w9tvevxiXUtY8/ICrU+l26jHmhJDra3Vwo=`.
The upstream license and source notices remain in the module directory.

The portable command host uses the selected syntax parser, never the interpreter.
P04 adds bounded parser, brace-classification and static quote-removal operations:
allocation and recursive-entry reservations precede parser growth. Exact selected-tree node/depth admission stays
in the host before constructing its plan; transient parser wrappers are scratch.
The host reserves the full scratch allowance against its aggregate budget before
parsing. An exhausted scratch budget returns a typed refusal with no partial tree.
The modifications are recorded in [mvdan-sh.patch](mvdan-sh.patch). Only
`syntax.Parse` activates `WithBudget(Budget{MaxBytes: ...})` or the shared
`WithBudget(Budget{Pool: ...})`; other parser entry
points explicitly refuse that option. The host's selected allowance is 32 MiB
from `evaluation_memory_bytes`, reserved before constructing the parser. All
active parse calls, retained state and old/new host-plan overlap must fit the
host aggregate reservation; a successful parse does not independently grant
memory or effects.

The meter reserves payload/backing capacities before `new`, `make`, slice growth,
byte-to-string conversion and diagnostic formatting. It conservatively rounds
allocation payloads and retains old/new capacities to bound allocator churn as
well as live parser payload. Fixed parser buffers count too. Parser, lexer and
arithmetic methods reserve 4 KiB for each active call, including the next-call
prologue, argument spills and call bookkeeping, before recursive body work.
Their selected source uses fixed locals smaller than this envelope; variable
payloads are charged independently. Entry/exit guards use direct deferred calls,
not allocating closure objects per input byte. This is a byte reservation rather
than an extra node/frame-count or grammar-depth limit. Allocator/runtime global
metadata and process RSS are distinct measurements, not represented as exact
per-parser heap measurements.

The private budget abort is recovered by `Parse` and bounded brace classification;
unrelated panics propagate.
Scratch exhaustion returns `BudgetError` with the refused byte reservation and
no partial tree. `syntax/budget_test.go` covers exact reservation boundaries,
valid 8192-node/depth-64/1-MiB units, dense and malformed deep syntax, parser reuse,
unsupported entry points and unrelated panics. The exact selected-tree limits
remain host admission checks before host-plan allocation. These source-level
controls do not establish installed-platform qualification.


`syntax.NewAllocationBudget` creates one invocation-local logical ledger. Pass it
through `syntax.Budget.Pool`, `syntax.ClassifyBraces`, `expand.StaticLiteral` and
host lowering; caller allocations use `ReserveHeap` or `ReserveBytes` before
allocation. `UsedBytes` reports the shared ledger, while `Parser.ScratchBytes`
reports the parser's cumulative charged heap after active frames unwind. Old and
new backing storage remains charged across phases until the source unit is
discarded. The isolated `MaxBytes` parser API keeps independent reservations when
a parser is reused. These are conservative allocation reservations, distinct from
process RSS, allocator-global metadata and the host's retained-program ledger.

`ClassifyBraces` runs the same selected `SplitBraces` algorithm with guarded
allocation. It reports actual unquoted expansion without mutating the input;
malformed and quoted braces retain their literal semantics. Existing upstream
`SplitBraces` consumers remain for the general expansion API and its fixtures.
`StaticLiteral` admits only literal and ordinary single/double-quoted parts;
parameters, substitutions, dollar quotes and tilde/brace expansion are deferred.
It has no environment or filesystem callback. A single pre-sized output replaces
the general expander's fragment-per-escape storage. Both paths share the selected
escape-removal walker, including Unicode escapes and double-quote distinctions;
the host does not implement a second quote grammar. The new fixtures cover a
1 MiB escaped word, malformed brace amplification, detached output, parser reuse,
and a shared parse/quote/lowering allocation at its exact ceiling and one byte over.

To reproduce or update this copy, verify the complete release archive against the
module checksum above, extract it into a clean directory, and apply
`mvdan-sh.patch` with `patch -p1`. The patch includes every modified upstream file
and all added budget/helper/test files; compare the resulting module tree byte for
byte with this copy. Regenerate the complete patch against that verified upstream
tree whenever owned module files change. Review the pinned commit/checksums,
`lycaon/go.mod` replacement and this record together. Run the selected syntax and
expand suites through root `./task test:digest -- mvdan.cc/sh/v3/syntax
mvdan.cc/sh/v3/expand`, followed by the affected host parser contracts. Remove the
local replacement only when upstream supplies all required bounds and semantics.

The ordinary selected-library run is a unit/seed-corpus check. Upstream short-mode
external-shell confirmation, quote seeds requiring the declared Bash version,
unquotable quote seeds, invalid-shell parse/print seeds and unsupported language
variant seeds have separate skip conditions. A passing ordinary run does not
establish reference-shell confirmation, sustained fuzzing, native runtime or
installed-platform qualification. No new helper fixture skips its acceptance.
