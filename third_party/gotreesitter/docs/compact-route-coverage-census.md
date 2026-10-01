# Compact-route coverage census

> Historical snapshot: This report records the 2026-07-20 smoke census.
> See [compact-route-real-corpus-matrix.md](compact-route-real-corpus-matrix.md)
> for the current pinned-corpus matrix.

Date: 2026-07-20. Base commit: `7a43c9cb` (origin/main). Branch:
`codex/compact-coverage-census`.

## Scope

The Phase-3 admission scorecard (`admission_scorecard_test.go`,
`TestAdmissionCandidateScorecard206`, landed with PR #417) drives the
compact candidate route across all 206 registered grammars and sorts
each into PASS, DIVERGE, FALLBACK, SKIP, or ERROR. It reports 48
byte-exact PASS, 153 FALLBACK, 5 SKIP, 0 DIVERGE. The FALLBACK count
splits into two coarse buckets by raw error text: 90 "not sole exact
EOF" and 63 "did not accept EOF". This census replaces those two
buckets with a fine-grained decline mechanism, per language, and
answers two questions the coarse buckets could not:

- which scheduler mechanism actually declines each language, so the
  burn-down work can target the highest-count mechanism first; and
- whether the scorecard's trivial one-line smoke fixtures understate
  real-world decline rates, by rerunning ten currently-PASSING
  languages against real corpus files.

This census does not change what the compact route accepts, declines,
or materializes. It adds an opt-in, diagnostic classification layer
and reports what it measured.

## Method

### Instrumentation

Two call sites collapsed every compact-route decline before this
census: `requireParserCoreFreshFullAcceptance`
(`parsercore_phase0_fresh_full_runner.go`) folded every decline into
one of two fixed strings, discarding the boundary and detail the
compact scheduler (`parsercore_phase0_driver.go`) had already computed
before giving up. `admissionCandidateDeclineReason`
(`admission_switch_candidate.go`) rendered the boundary for hard
scheduler errors (for example a cap hit) but had no mechanism-level
grouping.

`admission_census.go` (new file, `gts_parsercorephase0` build tag) adds
a single classification function,
`admissionCensusClassify(boundary, detail)`, that maps any decline
(hard error or soft scheduler-recorded stop) onto one of eleven
mechanism classes: `recovery-entered`, `extra-chain-shape`,
`extra-shift-shape`, `repetition-shift-class`, `zero-width-shift`,
`external-scanner-state`, `mode-lex-feature`, `cap-hit`,
`multi-derivation-at-eof`, `eof-byte-short-frontier`, and
`scheduler-frontier-shape` (structural catch-all), plus
`other-with-detail` for anything unrecognized. It is gated behind
`GTS_ADMISSION_CENSUS=1`:

- unset (the default, and every existing test's default), the two
  edited call sites are byte-identical to before this file existed —
  verified below;
- set, every decline's reason string carries a `[mechanism=...]` tag
  the census aggregation reads.

Cost when off: one `sync.Once`-cached boolean read on the decline path
only, which was already the cold, diagnostic path (a FALLBACK, never a
successful parse). The default build does not compile this file at
all (`gts_parsercorephase0` tag).

Files changed:

- `admission_census.go` (new) — the classifier and the two
  census-only decline constructors.
- `parsercore_phase0_fresh_full_runner.go` — two `if
  admissionCensusEnabled() { ... }` branches added immediately before
  the two existing coarse-error returns in
  `requireParserCoreFreshFullAcceptance`; the coarse returns are
  otherwise untouched.
- `admission_switch_candidate.go` — `admissionCandidateDeclineReason`
  gained one more `if admissionCensusEnabled()` branch alongside its
  existing `errors.As` unwrap.

### Runs

```
GTS_ADMISSION_SCORECARD=1 \
  go test -tags gts_parsercorephase0 -run TestAdmissionCandidateScorecard206 -v .
```

reproduces the shipped scorecard numbers unchanged (see Gates below).
Adding `GTS_ADMISSION_CENSUS=1` to the same command produced the
fine-grained table this report is built from. Both runs used the base
commit above with no other changes.

For the depth check (task 4), a temporary driver test read ten real
corpus files and ran the same PASS/DIVERGE/FALLBACK/SKIP classification
used by the scorecard. It ran once, its output is recorded below, and
it was deleted before this branch's commit — it is not part of the
shipped instrumentation.

## Baseline results (unchanged)

| Status | Count |
|---|---:|
| PASS | 48 |
| DIVERGE | 0 |
| FALLBACK | 153 |
| SKIP | 5 |
| ERROR | 0 |
| Total | 206 |

SKIP (5, not DFA-routable — token-source grammars): authzed, c, cpp,
java, json.

## Fine-grained decline census (153 FALLBACK languages)

| Mechanism | Count | % of FALLBACK | Reason class | Est. tractability |
|---|---:|---:|---|---|
| `eof-byte-short-frontier` | 90 | 58.8% | scheduler-gap | High — one uniform shape, but see the depth-check caveat below |
| `zero-width-shift` | 33 | 21.6% | scheduler-gap | High — one uniform shape, confirmed to recur at real-corpus depth |
| `repetition-shift-class` | 27 | 17.6% | scheduler-gap (documented missing feature) | Medium — the scheduler explicitly declines it as unimplemented; confirmed the single most depth-persistent mechanism |
| `mode-lex-feature` | 2 | 1.3% | scheduler-gap (lex mode) | Low volume, architectural — needs production recovery-lexer semantics |
| `scheduler-frontier-shape` | 1 | 0.7% | multi-derivation (adjacent) | Unknown — needs an individual look at `robot` |

Zero languages declined for `recovery-entered`, `extra-chain-shape`,
`extra-shift-shape`, `external-scanner-state`, `cap-hit`, or true
`multi-derivation-at-eof` (an accepted head carrying more than one
exact derivation, or more than one accept action firing during a run).
The scorecard's one-line smoke fixtures are, by construction, valid
and short: they never force recovery, an external-scanner-state
mismatch, a cap, or a genuine grammar ambiguity. Read this census as a
map of the reachable mechanism space at trivial depth, not a complete
inventory of every mechanism the compact route can decline on.

### Correcting a prior assumption: `eof-byte-short-frontier` is not multi-derivation

The campaign's coarse framing labeled the 90-language "not sole exact
EOF" bucket as multi-derivation. It is not. Every one of the 90
declines in this bucket carries `ExactPaths == 1` and exactly one
accept action (`Accepts == 1`, `Work.Accepts == 1`) — a single,
unambiguous derivation. The strict acceptance gate declines it purely
because the accepted head's own byte offset is short of the source
length. The shortfall is exactly one byte in every one of the 90
cases, and every one of the scorecard's 206 smoke fixtures ends with
exactly one trailing newline character
(`grammars/smoke_samples.go`). The compact scheduler correctly
authenticates the EOF token at the true end of the source (the token
identity check passes), but the accepted head's own boundary snapshot
does not include that final trailing byte. Materialized, the resulting
tree's root span would end one byte short of the source — a real gap,
not a false positive from the gate, but a narrow, mechanical one: a
single-byte trailing extra immediately before EOF, not an ambiguous
grammar.

## Top-5 burn-down worklist

Ranked by measured FALLBACK unlock count from this census, with the
real-corpus depth check (below) used to re-weight priority where the
raw count and the depth signal disagree:

1. **`eof-byte-short-frontier` — 90 languages.** Largest raw bucket by
   a wide margin (59% of all FALLBACK). One uniform shape: the
   accepted head's byte offset is exactly one byte short of the
   source length, in every case tied to the smoke fixture's own single
   trailing newline. Caveat: this mechanism did not appear in any of
   the ten real-corpus depth-check languages (below) — those files hit
   an earlier decline (`repetition-shift-class` or `zero-width-shift`)
   before ever reaching the final byte, so fixing this mechanism alone
   is a certain, verifiable win on the trivial-fixture set but an
   unproven one at file-scale until the two mechanisms below are also
   closed for the same language.
2. **`zero-width-shift` — 33 languages.** One uniform shape ("generic
   scheduler ordinary shift is not positive-width" on a non-EOF cell).
   Confirmed to recur at real-corpus depth: 3 of 10 depth-check
   languages (bash, julia, markdown) hit this mechanism on real files.
   Ecosystem weight is high: the 33-language list includes javascript,
   typescript, tsx, python, ruby, kotlin, scala, swift, haskell, toml,
   and yaml.
3. **`repetition-shift-class` — 27 languages.** A scheduler-documented
   missing feature ("generic scheduler does not support repetition
   shifts", requiring production frontier-suppression semantics). This
   is the single most depth-persistent mechanism measured: 6 of 10
   depth-check languages (elixir, svelte, dockerfile, make, xml, hcl)
   hit it on real files — twice the hit rate of `zero-width-shift` and
   the only mechanism to dominate the depth check despite ranking
   third by raw trivial-fixture count.
4. **`mode-lex-feature` — 2 languages** (doxygen, vhdl). Declines on
   no-lookahead tokens, which only the production recovery-lexer path
   can honor. Low volume; treat as a low-priority architectural item,
   not a quick win.
5. **`scheduler-frontier-shape` — 1 language** (robot). The scheduler
   found a mixed accepted/shifted frontier ("generic scheduler
   requires a sole homogeneous accept frontier") — the one case in
   this entire census that looks like a genuine derivation fork rather
   than a missing feature. Worth an individual look before assuming it
   generalizes to any other language.

Read items 2 and 3 as the priority-corrected top of the list for
real-world impact: the raw trivial-fixture count ranks
`eof-byte-short-frontier` first, but the depth check shows
`repetition-shift-class` and `zero-width-shift` are the two mechanisms
that actually block real files, and together they cover flagship
ecosystem languages (javascript, typescript, python, ruby, elixir,
xml, hcl, dockerfile, make) that `eof-byte-short-frontier` does not
touch at depth.

## Depth check: real corpus vs. trivial smoke fixtures (task 4)

The scorecard's fixtures are one-line smoke snippets by design (its
own doc comment calls this "reachability reconnaissance ... not a
fidelity proof"). This section tests whether that limitation hides a
coverage cliff for the 48 languages currently reporting PASS. Ten
PASS languages, chosen for ecosystem importance, were rerun with a
real corpus file each (480 bytes to 12,438 bytes, sourced from
`cgo_harness/corpus_real/` in the main worktree and the repository's
own `cgo_harness/docker/Dockerfile`) through the same
PASS/DIVERGE/FALLBACK classification the scorecard uses.

| Language | Fixture bytes | Trivial-fixture status | Real-corpus status | Depth mechanism |
|---|---:|---|---|---|
| go | 12,438 | PASS | **PASS** | — (byte-exact digest match) |
| bash | 6,239 | PASS | FALLBACK | `zero-width-shift` |
| elixir | 550 | PASS | FALLBACK | `repetition-shift-class` |
| julia | 9,245 | PASS | FALLBACK | `zero-width-shift` |
| svelte | 9,182 | PASS | FALLBACK | `repetition-shift-class` |
| dockerfile | 572 | PASS | FALLBACK | `repetition-shift-class` |
| make | 4,765 | PASS | FALLBACK | `repetition-shift-class` |
| xml | 480 | PASS | FALLBACK | `repetition-shift-class` |
| markdown | 3,395 | PASS | FALLBACK | `zero-width-shift` |
| hcl | 9,182 | PASS | FALLBACK | `repetition-shift-class` |

**Verdict: no DIVERGE.** Every one of the nine languages that lost PASS
status at depth failed closed — production served the parse exactly as
the admission switch's fail-closed design intends. This is not a
correctness violation, and the admission gate's core safety property
(never materialize a silently-wrong compact tree) held on every one of
these ten real files.

**It is still a critical finding for coverage, not correctness.** Nine
of ten sampled PASS languages (90%) lose PASS status at realistic
depth. Only go — the one grammar the compact route was originally
built and tuned against (`BenchmarkParserCoreFreshFullCanonical`, the
four canonical Go fixtures) — held. The scorecard's headline "48
byte-exact" count is real for the fixtures it tested, but this sample
shows it does not predict real-file coverage for any language this
census sampled other than Go. Any claim built on the 48-language PASS
count needs that caveat until `repetition-shift-class` and
`zero-width-shift` — the two mechanisms this depth check actually
hit — are closed.

## Gates

- Default build (`go build ./...`, `go test .`) unaffected: the new
  file (`admission_census.go`) and every edit are behind the
  `gts_parsercorephase0` build tag; the default build does not compile
  them.
- Tagged build green: `go build -tags gts_parsercorephase0 ./...`,
  `go vet -tags gts_parsercorephase0 ./...`, and
  `go test -tags gts_parsercorephase0 .` all pass, along with
  `go test ./internal/...`.
- Scorecard reproduces unchanged with instrumentation off: a byte-diff
  of `TestAdmissionCandidateScorecard206 -v` output before and after
  this branch's changes (`GTS_ADMISSION_CENSUS` unset in both) differs
  only in the wall-clock timing lines. The summary line is identical:
  `PASS=48 DIVERGE=0 FALLBACK=153 SKIP=5 ERROR=0 total=206`.

## Full per-language table

Grouped by decline mechanism. PASS (48) and SKIP (5) languages are
listed once each; every language below is a FALLBACK.

### `eof-byte-short-frontier` (90) — scheduler-gap, high tractability

ada, angular, apex, arduino, awk, bibtex, bicep, blade, brightscript,
c_sharp, cairo, capnp, circom, corn, cpon, css, cuda, cue, d, dart,
devicetree, dhall, dot, ebnf, eds, elisp, elsa, enforce, erlang,
facility, faust, fennel, fidl, firrtl, forth, fsharp, gleam, glsl, gn,
godot_resource, graphql, groovy, hack, hare, heex, hlsl, html, jq,
json5, jsonnet, less, linkerscript, llvm, lua, luau, meson, move,
nickel, nix, objc, ocaml, pem, perl, php, pkl, prisma, promql, proto,
puppet, ql, regex, rego, ron, rust, scss, smithy, solidity, sparql,
sql, squirrel, tablegen, teal, textproto, thrift, turtle, verilog,
vue, wgsl, wolfram, zig

### `zero-width-shift` (33) — scheduler-gap, high tractability

agda, astro, caddy, cobol, elm, foam, fortran, gdscript, haskell,
haxe, javascript, just, kotlin, mojo, nginx, nim, org, powershell,
pug, python, r, rescript, rst, ruby, scala, starlark, swift, tlaplus,
toml, tsx, typescript, typst, yaml

### `repetition-shift-class` (27) — scheduler-gap, medium tractability

asm, bass, clojure, cmake, comment, cooklang, diff, djot, eex,
embedded_template, gitattributes, gitignore, http, hurl, janet,
jinja2, jsdoc, kdl, markdown_inline, norg, properties, racket,
scheme, ssh_config, todotxt, wat, yuck

### `mode-lex-feature` (2) — scheduler-gap, low volume / architectural

doxygen, vhdl

### `scheduler-frontier-shape` (1) — multi-derivation (adjacent), unknown

robot

### PASS (48)

bash, beancount, bitbake, chatito, commonlisp, crystal, csv, cylc,
desktop, disassembly, dockerfile, dtd, earthfile, editorconfig,
elixir, fish, git_config, git_rebase, gitcommit, go, gomod, hcl,
hyprlang, ini, julia, kconfig, ledger, liquid, make, markdown, matlab,
mermaid, ninja, nushell, odin, pascal, prolog, purescript,
requirements, svelte, tcl, templ, tmux, twig, uxntal, v, vimdoc, xml

### SKIP (5) — not DFA-routable (token-source)

authzed, c, cpp, java, json

### Preferred token-source DFA shadow audit

The five SKIP entries still use token sources through the grammar registry.
Each embedded language also has DFA tables. C++ also has its required scanner.

`TestAdmissionCandidateTokenSourceEntriesExposeDFASmoke` records the latent DFA
capability. It does not change the preferred backend or the SKIP ratchet.

`TestAdmissionCandidateDFASmokeMatchesPreferredTokenSource` adds a separate
backend-equivalence receipt. The field-aware digest includes:

- Node types and fields.
- Byte and point spans.
- Named, extra, missing, and error flags.
- Child counts and structure.

All five smoke fixtures route through the compact parser. Each result matches
the preferred token-source tree.

The bounded real-corpus audit used files of 16,383 bytes or less:

- C: both files declined the compact route. Their DFA fallback matched the
  token-source tree.
- C++: all three files declined. The DFA fallback differed from the
  token-source tree on two files.
- Java: one file routed and matched. One file declined, and its DFA fallback
  matched.
- JSON: all three files routed and matched.
- Authzed: the corpus has no fixture. The canonical smoke fixture is the only
  current receipt.

Keep all five rows as SKIP. Production promotion needs one public shadow route.
That route must try compact DFA parsing and use the token source after a
decline. It must not expose the ordinary DFA fallback.

Add a small canonical Authzed corpus fixture before a depth promotion.

## Follow-up

- The top-2 real-world burn-down items by this census are
  `repetition-shift-class` and `zero-width-shift`, not the
  raw-count-leading `eof-byte-short-frontier`. Any follow-on work
  should prioritize accordingly.
- `eof-byte-short-frontier` still merits a fix: it is the largest
  single bucket and the root cause looks narrow (a trailing
  single-byte extra before EOF is not attached to the accepted
  derivation's byte span), but its yield at file-scale depends on
  fixing the other two mechanisms for the same languages first.
- `robot` (`scheduler-frontier-shape`) is the one case in this census
  that looks like a genuine derivation fork. It deserves an individual
  trace before generalizing any fix from the other four mechanisms to
  it.
- This census sampled ten depth-check languages, chosen for ecosystem
  importance and PASS status, not all 48 PASS languages. A full
  48-language depth pass (a real corpus file per currently-PASSING
  language) is the natural next census and would confirm whether the
  90% PASS-loss rate holds across the whole PASS set or was an
  artifact of the ten sampled languages.
