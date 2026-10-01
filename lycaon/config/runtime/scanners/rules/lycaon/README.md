# Bundled security rules

The shipping selection is the explicit file list in `../../opengrep-gates.yaml`.
Pinned upstream sources remain in `../vendor/`; a vendor refresh cannot silently
activate another rule. First-party rules are CC-BY-4.0. Provenance and corpus paths
are recorded in `../../rules-provenance.yaml`.

Security rules identify concrete unsafe operations, external input reaching a sensitive sink,
or explicit insecure configuration. Comments, TODOs, normal API use, and absent
optional hardening are not vulnerabilities. ERROR is used for a concrete unsafe
operation; WARNING requests review where exploitability depends on context.

Native patterns provide syntax context. The selected analysis mode in
`../../opengrep-gates.yaml` is `intrafile`, including supported helper and closure
flows within a file. Guarded taint signatures are disabled. Taint rules model declared
sources, sinks, and sanitizers; parameter binding, argument-vector execution, and
data parsing have negative examples. Add framework APIs deliberately; a wrapper
or cross-file flow is not automatically covered.

Groovy rules use native `groovy` patterns. Perl and PowerShell currently select
path-scoped `generic` rules even though the maintained engine provides native
parsers for both languages. Native parser availability does not expand those
selected rule models. Cairo account checks, JSP, and Vue template checks also use
path-scoped generic patterns.
The runner excludes grammar-recognized comments and literal text from generic
findings while retaining executable interpolation.
A parser that cannot finish fails the scan instead of silently suppressing results.
JSP comment boundaries are handled by its rule because JSP is an additional
embedded format outside the editor grammar list.

HTML and Vue executable script blocks are projected with an HTML tokenizer and
analyzed using the JavaScript/TypeScript rules. Projections preserve original byte
offsets and newlines. HTML comments, data scripts, and external `src` blocks are
excluded. Script language mismatches fail explicitly. Template event handlers,
CSS, external script contents outside scan scope, and framework template
compilation are not inferred from this projection.

`coverage/security.yaml` contains internal target observations, including selection
of HTML script hosts. The host discards these before publishing findings or warnings.
Capability limits belong in the supported-language documentation, not per-file notices.

Default rules omit context-free checksums, optional hardening, public-asset policy,
and ordinary evaluation or serialization. Taint sources represent modeled external
input. Sanitizers are sink-specific. Known-safe cases must stay silent without
suppressing the corresponding unsafe control. See the precision policy in
`docs/scan-supply-chain.md` at the repository root.

Run `./task test:lycaon-rules` from the repository root. Use
`./task test:lycaon-rules -- -language python` for one language. The Go conformance
runner compiles the same bundle as production and requires positive and negative
examples for every security rule, including selected vendor rules. Each vulnerable
example is also tested inside a comment and after an unrelated comment. Expected
files must actually appear in the engine's scanned paths. Unexpected findings,
missing findings, parser diagnostics, and a missing engine fail the task.

The isolated corpus lives in `test/testdata/opengrep/` under the Go module. It is
source data for the scanner; fixture programs are never executed. Test cases carry
explicit rule IDs so dropped rules and stale expectations fail validation.
