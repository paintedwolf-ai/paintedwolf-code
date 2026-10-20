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
P04 adds a narrow parser scratch budget patch: allocation and recursive-entry
reservations precede parser growth. Exact selected-tree node/depth admission stays
in the host before constructing its plan; transient parser wrappers are scratch.
The host reserves the full scratch allowance against its aggregate budget before
parsing. An exhausted scratch budget returns a typed refusal with no partial tree.
The modifications are recorded in [mvdan-sh.patch](mvdan-sh.patch). Only
`syntax.Parse` activates `WithBudget(Budget{MaxBytes: ...})`; other parser entry
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

The private budget abort is recovered only by `Parse`; unrelated panics propagate.
Scratch exhaustion returns `BudgetError` with the refused byte reservation and
no partial tree. `syntax/budget_test.go` covers exact reservation boundaries,
valid 8192-node/depth-64/1-MiB units, dense and malformed deep syntax, parser reuse,
unsupported entry points and unrelated panics. The exact selected-tree limits
remain host admission checks before host-plan allocation. These source-level
controls do not establish installed-platform qualification.

## Native predicate ERE execution

`regexp2/` is the runtime source subset of the already selected
`github.com/dlclark/regexp2/v2` v2.5.1, commit
`0ec61737c1e8483bb16083a916fa9a3bd21f91bb`.
The complete release archive was verified before extraction against
`h1:E5Ug7Dh264W1ymdySmiHNcDG7fmsR307APCE5R07a20=`; its module metadata is
`h1:avUrQvPaLz2DrFNHJF0taWAFFX2C1GMSSoeiqFjcBmU=`.
The upstream license remains included. `UPSTREAM-MANIFEST.json` records every
verified archive member, its SHA-256 and whether it is included. The subset keeps
root runtime Go sources, `syntax/`, `helpers/`, module metadata and license;
upstream tests, test data, code generation, benchmarks, CI and documentation are
omitted. Synthetic host fixtures exercise the selected bounded entry point.

The P08 patch adds bounded boolean execution of a host-validated POSIX ERE tree.
It does not select the P13 PCRE grammar or expose additional regex syntax.
Existing upstream text parser/matcher entry points retain their behavior.
The patch and exact metering contract are recorded alongside the selected source.

The predicate grammar adapter also retains the selected Go1.26.4
`regexp/syntax/parse.go` and generated `perl_groups.go`, with the Go BSD license,
under `regexp2/syntax/posix/`. Their exact upstream hashes and tag are recorded
there. Parser nodes use the standard exported AST types; the grammar algorithm
is unchanged. The narrow patch threads the operation meter through the existing
parser/helper loops, class sort comparisons and growth sites, and reserves
source-bounded recursive scratch before parser entry. This closes the otherwise
uncancelable/unmetered validation step. It does not change standard-library
consumers elsewhere in the host.
