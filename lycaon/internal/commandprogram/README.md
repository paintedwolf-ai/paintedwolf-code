# Portable command operations

This package contains typed operation bindings, program-child attribution,
source-unit capture, byte streams, producer outcomes and native predicate
execution. A binding still invokes the existing subsystem's complete authority,
effect, evidence and recovery lifecycle. Package membership creates no owner or
authority. The command catalog remains the source of syntax and numeric bounds.

## Evaluator ports

`ports.go` holds the seams the evaluator is built against. `OperationRequest`
carries the typed plan statement and the complete expanded argv as `Fields`
(value plus source-word span); `Streams` reach the owner through native dispatch.
`Expander` performs field, scalar and pattern expansion under the evaluator's
`ExpansionScope`. `ProgramRecorder` is the receipt owner's port over the
`toolcontract` program receipt records, so this package stays free of storage.
`OutputPublication` opens a redirect destination, stages bytes and publishes or
abandons them through the destination owner. `toolcontract.ProgramMemory` is the
one aggregate memory admission used by the parser, snapshots, predicates,
streams and native dispatch.

`CatalogBinder` binds argv to one catalog `CommandSpecification`: exact
spellings, `--long[=value]`, clustered short options with attached or following
values, the catalog's `shorthand` (`-N` or `-SIGNAL`), `--`, and permutation
unless `operands_end_options` is declared. Find-style primaries bind whole words.
The bound record keeps each form's typed values; approval review presents that
record with the operation identity rather than a legacy tool schema.

## Native predicates

`BooleanBuiltin` implements `true`, `false` and `:` after ordinary scalar/argv
admission. Arbitrary operands, including dash-prefixed words, remain data.
`PredicateRuntime.TestBuiltin` implements `test` and `[`. It validates the whole
argv expression before probing paths: empty input is false, a single operand is
its nonemptiness, binary comparison operands remain data, and compound forms use
`!` before `-a` before `-o`, with parentheses. Unlike legacy argument-count
dispatch, this explicitly frozen precedence also applies to four-operand forms.
A one-string primary before a boolean connector may contain operator-shaped
data, as in `test -n -a x`. `[` requires its final `]`.
Decimal comparisons accept signed decimal digits and leading zeroes without
machine-integer overflow or arithmetic evaluation. The retained grammar defines
no locale or numeric-expression evaluation.

`TestExpression` evaluates the detached `[[` syntax tree directly. Structural
validation precedes evaluation; `&&` and `||` leave unreached words deferred.
The expansion port preserves ordered quoted fragments and performs no splitting,
pathname expansion or executable reparsing. For glob and ERE RHS operands,
quoted fragments are literal even inside an otherwise unquoted pattern.

Statuses zero and one are completed true/false predicates. Malformed syntax,
invalid integers and invalid patterns produce ordinary status two with incomplete
output. Authority, cancellation, changed-target and resource failures remain
errors; negation and short-circuiting cannot erase a reached host failure.
Status one does not acquire comparison-publication eligibility.

The filesystem port binds an actual invocation to its admitted `ReadSession`.
It resolves through existing project/host namespace authority and read globs,
then queries through the held-descriptor filesystem implementation. Metadata
queries do not require opening file content. `-L` inspects the final link entry,
including a dangling or outward-pointing link; following predicates retain the
existing contained-link traversal/refusal policy. A trailing directory separator
requires a directory. Access predicates ask the effective OS identity/ACL policy,
never route through `ResolveWrite`, and grant no future read, write or execute
operation. A missing admitted target is false; authority refusal is an error.
Windows retains its existing refusal of reparse traversal, rather than reporting
a confined target it did not observe.

## Pattern resources and selected dependencies

The shell's existing glob translator defines string glob syntax. The selected
maintained Go POSIX parser defines the ERE grammar. Counted repetitions are
lexically tagged for validation, then restored in the typed tree; valid counts
above the standard parser's convenience ceiling remain counted expressions.
For boolean matching on L input scalars, finite bounds above L+1 are reduced
to L+1: any successful longer sequence contains a zero-width iteration that
can be removed or duplicated at the same position. Captures are unobservable
and POSIX validation excludes backreferences, so text/assertion semantics are
preserved. Fixtures cover nullable and anchored empty alternatives.
The bounded existing regexp2 VM consumes that typed tree. It exposes no capture
results, text-parser fallback or additional PCRE grammar. P13 engine/adapter
qualification remains independent.

ERE matching searches within the scalar. Dot and negated classes include LF;
`^` and `$` anchor the whole scalar. There is no locale-dependent collation. Collating/equivalence bracket atoms
`[[.x.]]` and `[[=x=]]` name one Unicode scalar with identity equivalence;
ranges use scalar ordering. Unknown or multiscalar collating names are invalid.
The same opt-in scalar profile feeds glob and ERE matching.
Quoted ERE fragments remain literal substrings rather than implied equality.

Production runtimes and pattern adapters reserve working storage from the
invoking program's `toolcontract.ProgramMemory` before allocating. It observes
cancellation and refuses exhausted same-program capacity without waiting for
reservations already held by that operation. Pure
fixtures must explicitly select `LocalBoundsOnly`; that option establishes no
aggregate qualification. Predicate AST nodes and fragment concatenation retain
reservations through the operation. Argv predicate operator nodes are utility
data, not selected shell syntax nodes; they use the admitted field count,
argv bytes and parser/evaluator memory reservations rather than borrowing the
syntax-unit AST depth ceiling. Lexical normalization reserves its bounded
old/new string and tag capacities before construction: for B submitted bytes,
`1024 + 8*(B+1)^2` covers the fixed maps plus at most B/3 tagged counts,
their old/new metadata and bounded string copies. This is a source-derived
reservation, rather than a separate numeric language ceiling. The selected POSIX
parser, writer and VM share incremental reservations and return all leases on
success or refusal. Allocation accounting includes old/new growth overlap and
retains cumulative operation payload reservations; it is not a Go heap/RSS cap.
Static catalog/Unicode tables and caller-owned expanded values remain with their
existing storage lifecycle; they are not claimed as allocations inside this lease.

The selected parser meters helper/scan loops, class comparisons, nodes, slices,
maps and recursive scratch. The writer meters traversal and code/table growth.
The VM meters instructions, scalar loops and stack/capture growth. The regex
work ceiling is a meter over these operations. The monotonic deadline and context
are checked before work/allocation and at bounded loop boundaries; this is
cooperative cancellation, with no abandoned match goroutine. All maxima come
from the frozen catalog. A resource refusal returns no successful match claim.
Snapshot hashes, licenses, omitted files and patches live in `third_party`.

Tests use synthetic fixtures. Standard Go regex comparisons are reference probes
for adapter equivalence. Passing unit tests do not qualify connected evaluator
wiring, real model behavior, installed operating systems or milestone Q22.

## Status and continuation

`OutcomeLatch` preserves the first typed host stop. Ordinary nonzero results
remain available to conditionals and sequences; authority refusal, interruption,
owner conflict/limits and incomplete required input stop admission before later
expansion or effects. `StatusControl` keeps the last executed/explicit exit status;
negation changes ordinary status only. Failed substitution prevents its containing
launch without turning output into syntax.

`SettlePipeline` retains every original stage result and uses the shared
rightmost-nonzero aggregation rule. Expected consumer closure requires the
existing executor classification or the exact native byte-endpoint close fact,
bound to adjacent dynamic execution IDs. It cannot hide another producer failure.
Typed completeness and eligibility remain separate from ordinary status and
verification evidence: native comparison status1 may publish, while unclassified
external status1 cannot. Destination review, revision checks, publication and
recovery remain with their existing operation domains.

The legacy external executor now reports the rightmost nonzero pipeline status,
instead of the first nonzero status. Its established SIGPIPE classification and
raw per-stage outcomes are retained. Real helper-process fixtures cover3/7/0,
and the document-owner fixture proves that a later actual refused review and
exit0 preserve an earlier accepted/saved mutation. Additional publication fixtures
use classified producer inputs and real document publication; they are not a
claim that future native grep/diff adapters or full command entry are qualified.
