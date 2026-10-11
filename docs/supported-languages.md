# Supported languages

Painted Wolf Code edits other people's codebases, so "which languages do we
support" is a product answer, not a property of this repo's own stack.

The editor supports **44 languages** through gotreesitter grammars for outlines,
structural search, rewrite, and syntax health. Security coverage is a separate
capability: **37 of 44** have selected security rules. JSP is also scanned as an
embedded application format.

Go-side SSOT: `filekind.SupportedLanguages()` in
[`filekind/supported.go`](../lycaon/internal/filekind/supported.go). Content-based
detection, the coverage tests, and this page all read that one list. The security
selection is
[`opengrep-gates.yaml`](../lycaon/config/runtime/scanners/opengrep-gates.yaml),
with rule files under
[`rules/lycaon/<lang>/`](../lycaon/config/runtime/scanners/rules/lycaon). Every
selected rule has vulnerable and safe behavioral examples in the
[language corpus](../lycaon/test/testdata/opengrep), which also checks comments,
source selection, and the absence of parser errors. A count measures the tested
rule selection, not complete vulnerability detection.

## Capability matrix

| Capability | Tool | Coverage |
|------------|------|----------|
| Security findings | `scan_*` (OpenGrep rules) | 37 / 44 |
| Symbol outline | `read(symbol=…)`, `list_dir` root map, `summarize` | 44 / 44 |
| Structural search & rewrite | `grep(structural: true)`, `code_rewrite` | 44 / 44 |
| Mutation syntax health | `write`, `edit`, `replace_lines`, `code_rewrite` | 44 / 44 |
| Syntax highlighting | Den source viewer + diff | 44 / 44 |
| Package registry | Egress baseline and package-action review | [Package registries](#package-registries) |

## The list — 44 languages

`Grammar` is the name to pass as `lang` to `grep` / `code_rewrite`. `OpenGrep
scan path` lists native aliases, the bundled generic-rule path, or `—` when no
security rules are selected.

Which file resolves to which grammar is a question for the code, not this page:
`filekind.LanguageForPath` is the answer the host uses, and it delegates to
`grammars.DetectLanguage` in the pinned gotreesitter release. Extension lists
belong to that upstream map.

| Language | Grammar | OpenGrep scan path |
|----------|---------|---------------|
| Apex | `apex` | `apex` |
| Bash | `bash` | `bash`, `sh` |
| C | `c` | `c` |
| C# | `c_sharp` | `c#`, `csharp` |
| C++ | `cpp` | `c++`, `cpp` |
| Cairo | `cairo` | bundled generic account rules |
| Circom | `circom` | `circom` |
| Clojure | `clojure` | `clojure` |
| Common Lisp | `commonlisp` | — |
| Dart | `dart` | `dart` |
| Dockerfile | `dockerfile` | `docker`, `dockerfile` |
| Elixir | `elixir` | `elixir`, `ex` |
| Go | `go` | `go`, `golang` |
| Groovy | `groovy` | `groovy` |
| Hack | `hack` | `hack` |
| HCL / Terraform | `hcl` | `hcl`, `terraform`, `tf` |
| HTML | `html` | script projection |
| JSON | `json` | `json` |
| Java | `java` | `java` |
| JavaScript | `javascript` | `javascript`, `js` |
| Jsonnet | `jsonnet` | `jsonnet` |
| Julia | `julia` | `julia` |
| Kotlin | `kotlin` | `kotlin`, `kt` |
| Lua | `lua` | — |
| Move | `move` | `move_on_aptos` rules; no Sui security model |
| OCaml | `ocaml` | — |
| Perl | `perl` | bundled `generic` rules¹ |
| PHP | `php` | `php` |
| PowerShell | `powershell` | bundled `generic` rules¹ |
| PromQL | `promql` | — |
| Protobuf | `proto` | — |
| Python | `python` | `python`, `py`, `python2`, `python3` |
| QL (CodeQL) | `ql` | — |
| R | `r` | `r` |
| Ruby | `ruby` | `ruby` |
| Rust | `rust` | `rust` |
| Scala | `scala` | `scala` |
| Scheme | `scheme` | — |
| Solidity | `solidity` | `solidity`, `sol` |
| Swift | `swift` | `swift` |
| TypeScript | `typescript` | `typescript`, `ts` |
| Vue | `vue` | script projection + generic template rules |
| XML | `xml` | `xml` |
| YAML | `yaml` | `yaml` |

¹ The selected Perl and PowerShell rules use OpenGrep's path-scoped `generic`
matcher. The maintained engine provides native parsers for both; the selected rules
do not use them. Parser availability and
selected security-rule coverage are separate capabilities; grammar-backed editor,
outline, search, and rewrite paths are independent of the scanner selection.

OpenGrep's `generic`, `none`, and `regex` are matching modes, not languages.
`move_on_aptos` and `move_on_sui` share the one `move` grammar.

Detection is not extension-only. Dockerfile resolves by filename, and Perl also
covers `cpanfile`, `Makefile.PL`, `Build.PL`, `Rexfile`, `ack`, and `latexmkrc`.
Suffixes that several languages claim resolve to exactly one: `.hh` is C++, not
Hack. The Perl SAST path additionally scans `.cgi` and `.fcgi` because the rules
match Perl-specific source shapes, while the editor resolves those files to
`bash`. One path is a rule-selection filter, the other is grammar detection, and
they are allowed to disagree.

### Not supported

| Language | Why |
|----------|-----|
| TSX (`.tsx`) | `.tsx` resolves to the `tsx` grammar, which is not in `filekind.SupportedLanguages()`, so `LanguageForPath` returns empty. Outline, structural search, rewrite, and mutation syntax health silently do not apply to any `.tsx` file, including Den's own source. Highlighting is unaffected; it uses the CodeMirror grammars. Plain `.ts` is fully supported. |
| Visual Basic (`vb`) | The engine can parse it, but no bundled security policy or editor grammar is selected. |

## Security rule scope

Common Lisp, Lua, OCaml, Scheme, CodeQL, PromQL, and Protobuf have no selected
vulnerability rules. Ordinary evaluation, serialization, queries, and schema
declarations do not establish an exploitable trust boundary; these formats remain
in the negative-regression corpus, and scans do not emit a notice for every file.
CodeQL query correctness belongs in query test fixtures or an explicit audit; an
expensive PromQL query is an availability assumption owned by the
[deployment model](https://prometheus.io/docs/operating/security/); an optional
Protobuf field or an absent validation annotation describes
[serialization semantics](https://protobuf.dev/programming-guides/field_presence/),
not application authorization.

Rules model declared data flows and explicit unsafe configuration. The default
catalog selects `intrafile` analysis, following supported helper and closure
flows within each file; guarded taint signatures remain disabled. Across the 26
fixtures in [helper-boundaries.yaml](../lycaon/test/testdata/opengrep-projects/helper-boundaries.yaml),
[helper-dead-closures.yaml](../lycaon/test/testdata/opengrep-projects/helper-dead-closures.yaml),
and [ordinary-helper-variants.yaml](../lycaon/test/testdata/opengrep-projects/ordinary-helper-variants.yaml),
this mode reports all 25 expected sink locations with no false positives;
intraprocedural mode reports 16, with eight false positives and nine misses.
These are regression cases, not a recall estimate. Same-file analysis costs more
time, and the dominant source file can remain serial, so additional workers do not
imply a proportional speedup.

Coverage is deliberately specific and does not claim whole-framework
authorization analysis. Local CLI arguments and environment configuration alone
do not establish an attacker-controlled boundary. Default rules omit context-free
checksum, public-asset, local-development, and optional-hardening advisories.
Cross-file wrappers, deployment policy, and unmodeled framework APIs require
additional rules or analysis. Representative boundaries:

- **Perl** matches direct CGI input at evaluation, shell, SQL, and two-argument
  `open` sinks. **Clojure** matches a registered Ring handler's request reaching
  code loading. **Cairo** checks unconditional account validation; **Move** checks
  an Aptos resource-withdrawal shape.
- **Python** models selected Django request collections, typed FastAPI request
  objects, and string route parameters on recognized FastAPI or APIRouter
  decorators. Dependency-provided values and Pydantic model fields remain
  unmodeled. The shell rule treats `shlex.quote` as a sanitizer without
  distinguishing argument values from a quoted executable name, so those
  whole-command cases can be missed.
- **JavaScript and TypeScript** require a recognized request boundary: Express and
  Fastify route callbacks, selected Next.js App Router exports with imported
  `NextRequest` or unshadowed web `Request` types, Pages Router default handlers
  with imported `NextApiRequest`, and Fastify plugins with an imported plugin type,
  `fastify-plugin` wrapper, or recognized registration. Objects merely named
  `request` do not. Same-file calls, closures, array mutations, and spreads retain
  bounded value information; alias or recursion limits produce partial-analysis
  diagnostics, which scan summaries group with exact locations.
- **Java and Kotlin** model import-resolved Spring request-binding annotations,
  excluding disabled `ModelAttribute` binding; Java also models typed Spring JDBC
  execution calls.
- **HTML and Vue** scripts use tokenizer-based projections into the TypeScript
  parser, with findings mapped to original coordinates. Vue template HTML
  injection has a separate generic check. Data-only and commented scripts are
  excluded.

New rules join the selected catalog once positive, safe, and comment regressions
pass `./task test:lycaon-rules`.

### Syntax-health boundary

Grammar support also protects source mutations. The host parses the complete
candidate buffer, not only the replacement text. New and previously clean files
must remain clean; already-broken files accept only strict incremental
improvement. Parser uncertainty is a rejection, not a silent bypass. Overlay
promotion requires the final merge plan to be fully clean. Files without a
supported grammar remain writable without a syntax verdict; structural rewrites
skip them.

The per-snapshot timeouts live in
[`source-parsing.yaml`](../lycaon/config/packs/painted-wolf/platform/host/source-parsing.yaml):
`validation_timeout_ms: 30000` for mutations and promotion, and
`analysis_timeout_ms: 5000` for structural search, outlines, and source analysis.
Both must be positive; unknown keys and invalid values are errors. A checkout
configuration selected by `LYCAON_CONFIG_ROOT` can override them. Mutable
configuration is re-read on the next parse, and outline cache keys include the
analysis timeout, so a corrected setting takes effect immediately. Request
cancellation propagates through source analysis; a canceled cached analysis
never becomes a source failure for a later request.

A timeout, cancellation, enclosing deadline, parser resource limit, or internal
failure produces an unavailable verdict with the reason, language, configured
timeout, elapsed time, source size, and last-token byte offset. Partial trees
never establish valid syntax or a complete symbol inventory. OAR owns the
recovery guidance: mutations preserve the existing file, promotion preserves the
overlay, and partial read/search results identify affected paths. Syntax errors
from a completed parse report locations, snippets, and missing token types.

The mutation tools and `promote_overlay` accept an explicit
`syntax_override_reason` for one invocation. A nonblank reason skips final syntax
validation and records `SYNTAX_CHECK_OVERRIDDEN` feedback with the reason and
affected paths; it does not carry into later calls. Structural rewrite selection
still requires a complete parse, and cancellation, write boundaries, content
screens, and other mutation checks still apply. The tool schema exposes the
option; teaching prompts and rejection recovery advice do not suggest it.

The rule is the same for all 44 languages. Python adds two narrow diagnostics for
its whitespace-sensitive grammar: indentation columns with visible tabs and
spaces near the edit, and a structural check for a `try` suite without an
`except` or `finally` clause when the bundled grammar recovers it as clean.

## Highlighting

The Den viewer uses the CodeMirror grammars in `package.json` (`lang-*` where they
exist, `legacy-modes` next), and languages with neither ship word-list stream
modes ([`codemirror-word-lang.ts`](../lycaon-den/src/components/source/editor/codemirror-word-lang.ts)):
keyword-accurate tokenizers, not parsers. Two consequences show up in the editor:

- **A parsed grammar reaches inside a file.** HTML, and the single-file component
  formats that borrow its shape, highlights embedded `<script>` and `<style>` with
  the JavaScript and CSS grammars, and a fenced block in Markdown highlights as
  whatever the fence names, by extension (` ```ts `) or by this page's label
  (` ```typescript `). An unlabelled fence stays plain.
- **A stream mode has no tree, so nothing to fold by.** Those buffers, and files
  with no grammar at all, fold on indentation. SCSS, Less, and XML stay on stream
  modes: the bundled CSS grammar does not parse nested preprocessor rules, and
  there is no bundled XML grammar.

## Writing a pattern

`grep(structural: true)` and `code_rewrite` match by syntax. `$VAR` binds one
node, `$$$VAR` binds a run of sibling nodes. Every supported language has
executable example patterns in
[`language_matrix_test.go`](../lycaon/internal/structrewrite/language_matrix_test.go),
which runs each against a fixture and asserts the captures; its matrix is guarded
against `filekind.SupportedLanguages()` in both directions, there and in
[`source_definition_language_matrix_contract_test.go`](../lycaon/test/contract/files/source_definition_language_matrix_contract_test.go).

**A pattern is one complete node, written the way the language spells it.** A
prefix does not work: `func Run(` is not a shape. Neither is a regex; pass that to
a text search instead.

- **Include the whole declaration**, body and all. Go wants its result type
  (`func $NAME($$$ARGS) $RET { $$$BODY }`); a method keeps its receiver.
- **Match the arity of what you are looking for.** A pattern with no result type
  will not match a function that has one. Use `$$$` to absorb what you don't
  care about: `class $C { $$$X void $NAME($$$ARGS) { $$$BODY } $$$Y }` finds a
  method anywhere in a class body, where the un-padded form matches only a class
  whose sole member is that method.
- **`$VAR` stays in its own grammar slot.** `$RET` in a result position will not
  bind a body block.
- **Quoted placeholders bind.** `"$V"`, the whole string being the placeholder,
  matches any string in that position, which is how JSON, YAML, and XML/HTML
  attribute patterns work. The trade is that a pattern cannot search for the
  literal text `"$V"`; use a text search for that.

When a structural search comes back empty, the note says whether any file in
scope had a grammar or whether the pattern simply did not occur.

### Where a shape has to change

Some grammars do not give a single node for the obvious shape. These are
verified alternatives, not gaps:

| Language | Instead of | Write |
|----------|-----------|-------|
| C# method | `void $NAME($$$ARGS) { $$$BODY }` | `class $C { $$$X void $NAME($$$ARGS) { $$$BODY } $$$Y }` — a bare one parses as a *local function*, not a method |
| JS/TS method | `$NAME($$$ARGS) { $$$BODY }` | `class $C { $NAME($$$ARGS) { $$$BODY } }` — shorthand is only a method inside a class |
| Dart call | `log($A)` | `log($A);` — the grammar has no call node; the statement carries the `;` |
| Dart function | `void $NAME($$$ARGS) { $$$BODY }` | match via the enclosing `class` — a top-level signature and body are siblings, not one node |
| C/C++ struct | `struct $NAME { $$$F };` | `struct $NAME { $$$F }` — the `;` is a sibling of the declaration |
| Jsonnet local | `local $NAME(…) = $$$BODY;` | `local $NAME($$$ARGS) = $$$BODY; $$$REST` — a bind includes the expression that follows |
| Move module | `module m::n { … }` | `module 0x1::n { … }` — a module path needs an address |

## Package registries

Every supported language is accounted for in
[`package-registries.yaml`](../lycaon/config/packs/painted-wolf/security/host/package-registries.yaml):
either the public registry its common package manager reads from, or an
`unregistered` entry saying why it has none. A test fails when a language is
missing or listed twice. The registry hosts are a quiet egress baseline below
Strict ([Security](security.md#public-package-registries)), and the
[manager catalog](security.md#newly-downloaded-package-code) reviews
commands that add, install, or run a named package from them.

Languages without a public registry: Apex (packages install into a Salesforce
org), Bash and Dockerfile (no language registry; images are pulled by the
container engine), Jsonnet, Move, and Scheme (dependencies come from source
forges), CodeQL (packs come from a general-purpose container registry), and the
data, markup, and query formats. Julia, R, Swift, Kotlin, Terraform, Protobuf,
and Common Lisp have registries but no package command the manager catalog can
read, because packages are added from inside the language runtime or a build
file; their registry traffic is still baseline.

## Adding a language

1. Confirm both halves: `grammars.AllLanguages()` has a grammar, and either
   `opengrep show supported-languages` lists it or compatible generic rules can
   be shipped with mandatory path filters.
2. Ship rules for it under `rules/lycaon/<lang>/` (or vendor them).
3. Add the grammar name to `supportedLanguages` in
   [`filekind/supported.go`](../lycaon/internal/filekind/supported.go) and a row to
   `languageShapeMatrix` in
   [`language_matrix_test.go`](../lycaon/internal/structrewrite/language_matrix_test.go).
   The conformance corpus check requires a security corpus for every entry, and
   the `languages` assignment in `scan-excludes.yaml` must name it. Account for it
   in the indexing [directory-priority catalog](../lycaon/config/runtime/source/directory-priority.yaml),
   using the shared group when it has no separate build-directory convention.
   Name its public registry in `package-registries.yaml`, or list it under
   `unregistered` with the reason. Then add it to the table above.
4. Run `./task test:digest -- ./internal/structrewrite/...`. If a pattern will
   not compile, the cause is almost always one of three things the engine already
   handles without a per-language table:
   - the grammar rejects a bare `$$$VAR` in a sequence slot (elision retries
     without the placeholders);
   - its lexer drops the placeholder rune (`expandoCandidates` retries `µ`, `$`,
     `_`, `Q`);
   - it needs a fragment context, which is a `patternWrappers` entry.

## Related

- [Scan supply chain](scan-supply-chain.md) — bundled scanners and the OpenGrep pin; [engine selection](../scripts/select-opengrep-release.sh) pins the maintained engine build
- [Dev tasks](dev-tasks.md) — `./task test:lycaon-rules` for rule regressions
- [Licensing](licensing.md) — rule provenance
