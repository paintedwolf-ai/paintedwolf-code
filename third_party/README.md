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


## Secret detector enumeration

`gitleaks/` contains the selected runtime source subset for
`github.com/zricethezav/gitleaks/v8` v8.30.1, commit
`8d1f98c7967eb1e79cb44ac6241a124e145d2165`.
The release archive was verified against its Go module checksum before extraction:
`h1:PmEvCfVI7ti9dV3s5aMZUY7sS2GxRvG3yzih7E+cS3w=`.
The module metadata checksum is
`h1:rTDwxRjufMKAkhTI/Mijd07nday1yOhf9qywjwz5Irw=`.
The upstream license and source notices remain in the module directory.

P07 adds a bounded detection entry around the same catalog, decoding, allowlist,
keyword and provenance algorithms. Finding/index enumeration must reserve its
storage before allocation. Existing unbounded entries retain their upstream
behavior; complete command stream projection uses the bounded entry only.
The narrow patch and focused controls record the supported accounting envelope.

The gitleaks snapshot omits upstream command generators, tests, fixtures, repository metadata, and unrelated documentation. It retains the transitive runtime packages, embedded default configuration, module metadata, and license. The complete original archive was verified against the module hash before selection; omitted files are not needed for the selected runtime and include upstream credential-shaped test examples.

The local [gitleaks.patch](gitleaks.patch) adds `DetectBounded` through the shared
`DetectContext` core. Its invocation-local meter reserves backing-store envelopes
before index enumeration, Unicode decoding, finding growth, required-rule joins,
and filter copies; it meters candidate counts and work separately. A reserved
sentinel distinguishes complete enumeration from truncation. Refusal returns no
partial findings. Keyword presence scans the exact configured keyword set rather
than retaining every Aho-Corasick occurrence. Legacy calls retain their original
unbounded behavior and use the same rule/codec/allowlist implementations.

These are conservative cumulative payload/allocation and selected-work
reservations, not exact process RSS or a Go heap ceiling. Static catalog storage,
regexp engine scratch, runtime metadata, logging infrastructure, and preexisting
caller-owned input are outside this meter. The stream caller must reserve its
owner buffers and the selected detector allowance before invoking it. Selected
catalog configuration retains its existing two decode passes. Differential tests
use synthetic custom catalog rules for codec/case/allowlist semantics, and
resource/cancellation controls require an error with no partial findings.

The bounded location adapter supplies newline byte positions to the original
location calculation through an iterator, retaining its coordinates without
materializing one newline array per rule. Enumeration returns unused sentinel
reservations only after proving how many candidate arrays were allocated;
actual capacities and conservative growth envelopes remain charged. The source
selection and exact upstream omissions/checksums are recorded in
[gitleaks.upstream.json](gitleaks.upstream.json).

The gitleaks patch uses zero-context hunks to record exact selected-source changes without treating upstream patch context indentation as repository whitespace.
