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
`Expander` performs field, scalar, pattern and here-document expansion under the
evaluator's `ExpansionScope`; `Substitution` is the evaluator's callback for a
reached `$()` or backtick unit. `ProgramRecorder` is the receipt owner's port over the
`toolcontract` program receipt records, so this package stays free of storage.
`OutputPublication` opens a redirect destination, stages bytes and publishes or
abandons them through the destination owner. `toolcontract.ProgramMemory` is the
one aggregate memory admission used by the parser, snapshots, predicates,
streams and native dispatch.

`CatalogBinder` binds argv to one catalog `CommandSpecification`: exact
spellings, `--long[=value]`, clustered short options with attached or following
values, the catalog's `shorthand` (`-N` or `-SIGNAL`), `--`, and permutation
unless `operands_end_options` is declared. Find-style primaries bind whole words.
The bound record keeps each form's typed values; approval review presents it
with the operation identity.

## Variables and expansion

`Variables` is one invocation frame: variables, export marks, positional
parameters, `$?`, `$!` and `$0`. The captured child environment seeds exported
entries; `Environment` applies one dispatch's `NAME=value` prefix without
changing the frame, and `Clone` gives an isolated group or substitution unit its
own copy. Entries are charged to `program_state_bytes` with old/new overlap and
bounded by `variable_count`, `variable_value_bytes` and `environment_bytes`.
Names follow the shell identifier grammar, NUL is refused and `IFS` cannot be
assigned; an `IFS` inherited from the environment is inert data.

`WordExpander` expands the detached plan's words, never upstream syntax objects.
Quote removal uses the selected `expand.RemoveQuotes` walker. Parameters are
`$name`, `${name}`, `${#name}` (Unicode scalars), `$0`, `$1`..`${10}`, `$#`, `$?`,
`$!`, `$@` and `$*`; unset values expand empty. Quoted `"$@"` keeps each operand
and expands to no field for no operands, `"$*"` joins with single spaces. Only
`~` and `~/` expand, from `HOME`; without it the tilde stays literal. Unquoted
expansion output splits on space, tab and LF; quoted output and literal text
never split. Values are data: metacharacters, substitution syntax and secret
reference tokens in a value remain argument text.

A field whose unquoted text has an active `*`, `?` or `[` is a pathname pattern.
Quoted and escaped characters are escaped in the pattern, so they match
literally. `commandsurface.MatchGlob` walks a `GlobFiles` view: matches are
sorted by bytes, hidden names need a leading dot in the component, `**` matches
nonhidden directories, and an unmatched pattern stays literal. The production
view is `projectpaths.GlobFiles`, which lists through the held-root confined walk
and admits a match only when the invocation's read authority admits that path.
Links are entries but never directories, so no walk traverses one. Matches and
directory visits are charged cumulatively to the program's `glob_matches` and
`traversal_visits`.

`Substitution` runs one reached unit through the evaluator's admission, dispatch
and receipts. Its stdout is bounded by `substitution_bytes`; trailing LF bytes
are stripped and NUL is refused. The context carries the nesting depth, bounded by
`substitution_depth`. An ordinary nonzero status is data the evaluator retains;
a port error or a refused, interrupted or failed unit returns `SubstitutionError`
with the unit's span and producer result, and no later part of the word expands.
Scalars are bounded by `variable_value_bytes`, here-documents by
`here_document_bytes`, fields by `argument_bytes`, `argv_bytes` and
`expanded_fields`. Live intermediate buffers are charged to the program's working
memory and `expansion_memory_bytes` and released when the word completes.
Every refusal is an `ExpansionError` with the catalog code and the word or part
span. Here-documents with a quoted delimiter are literal; `<<-` strips leading
tabs from body lines, not from expanded values.

## Evaluator

`Evaluate(ctx, plan, EvaluationPorts)` is the host statement scheduler over a
frozen `commandsurface.ProgramSnapshot`. It walks `;`, `&&`, `||`, `!`,
pipelines, braces and isolated `( )` frames, `if`/`elif`/`else`, `for`,
`while`, functions, `source -- PATH`, `test`/`[`/`[[`, `true`/`false`/`:`,
`exit`/`return`/`break`/`continue`, assignments, `export`/`unset`,
redirections, here-documents and statement-trailing `&`. Every simple command
is expanded through `Expander` when reached and dispatched through `Dispatch`:
catalog names bind first through `ArgvBinder` and `Invoke`; other programs and
`external --` use `Launch`. Unsupported option spellings refuse with the
binder's code and never fall back to an installed binary.

Status follows `StatusControl` and `OutcomeLatch`: the last executed status or
explicit exit, ordinary failures stay handleable, and refusal, interruption,
owner stops, invalid results and exhausted ceilings latch. There is no implicit
`set -e`. Pipelines run each stage concurrently in an isolated frame joined by
metered `BytePipe`s; each stage closes its own endpoints, every pipe is closed
before return, and `SettlePipeline` settles all stages. The native byte-endpoint
closure exception applies only to native producers. Functions share variables
and exit control with their caller and keep positional arguments local.
`( )`, `$( )`, pipeline stages and background jobs copy variables and functions
and fork the directory through `Dispatch.Fork`; a job also gets its own latch
and the streams `ProgramSupervision.StartJob` owns for it. The evaluator is the
`Substitution` port of the `WordExpander` it builds over `EvaluationPorts.Files`:
each reached unit runs in an isolated frame with its stdout going to the
expander's bounded writer. A nonzero unit status is retained, prevents the
containing dispatch (`AdmitSubstitution`) and becomes that command's status;
an `ExpansionError` latches as a refusal with its own code. `source` captures through `LoadSourceUnit`, preflights the
unit and runs it in the current frame; its pin is recorded in the outcome.

Before the first statement, preflight refuses reserved function names and any
construct whose owner port is absent, with `COMMAND_SYNTAX_UNSUPPORTED`. Every
reached statement is one `statement_visits` unit, each `while` test and `for`
item one `loop_iterations` unit, and function, source and substitution nesting
are bounded by depth ceilings. Operations, in-process native builtins, function
calls, substitutions, redirections, source units, isolated groups, pipelines
and jobs are child executions: each charges `child_executions` and holds the
catalog's per-execution trace settlement reservation until it settles.
Evaluator bookkeeping (assignments, declarations, branch and loop results) is
not a child execution. Frames keep their variables in `Variables`; a function's
`NAME=value` prefixes overlay its frame without mutating the caller. Child receipts belong to the
dispatcher's per-child admission; `ProgramOutcome` carries the bounded trace,
captured sources and job handles, and `Settlement` projects it onto the
receipt owner's aggregate record.

## Native predicates

`BooleanBuiltin` implements `true`, `false` and `:` after ordinary scalar/argv
admission. Arbitrary operands, including dash-prefixed words, remain data.
`PredicateRuntime.TestBuiltin` implements `test` and `[`. It validates the whole
argv expression before probing paths: empty input is false, a single operand is
its nonemptiness, binary comparison operands remain data, and compound forms use
`!` before `-a` before `-o`, with parentheses. This precedence also applies to
four-operand forms.
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
Windows refuses reparse traversal when it cannot observe a confined target.

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
results, text-parser fallback or additional PCRE grammar.

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

Tests use synthetic fixtures. Standard Go regex comparisons probe adapter
equivalence; connected command workflows have separate integration coverage.

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
verification evidence: native comparison status 1 may publish, while unclassified
external status 1 cannot. Destination review, revision checks, publication and
recovery remain with their existing operation domains.
