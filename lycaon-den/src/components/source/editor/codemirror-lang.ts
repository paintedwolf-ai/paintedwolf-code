import type { PrettyFormatTool } from "./format-settings.ts";
import { javascript } from "@codemirror/lang-javascript";
import { go } from "@codemirror/lang-go";
import { rust } from "@codemirror/lang-rust";
import { python } from "@codemirror/lang-python";
import { json } from "@codemirror/lang-json";
import { yaml } from "@codemirror/lang-yaml";
import { markdown } from "@codemirror/lang-markdown";
import { html } from "@codemirror/lang-html";
import { css as cssLang } from "@codemirror/lang-css";
import {
  LanguageSupport,
  StreamLanguage,
  type Language,
  type StreamParser,
} from "@codemirror/language";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { c, cpp, java, csharp, scala, kotlin, dart } from "@codemirror/legacy-modes/mode/clike";
import { sass } from "@codemirror/legacy-modes/mode/sass";
import { ruby } from "@codemirror/legacy-modes/mode/ruby";
import { swift } from "@codemirror/legacy-modes/mode/swift";
import { lua } from "@codemirror/legacy-modes/mode/lua";
import { r } from "@codemirror/legacy-modes/mode/r";
import { julia } from "@codemirror/legacy-modes/mode/julia";
import { clojure } from "@codemirror/legacy-modes/mode/clojure";
import { scheme } from "@codemirror/legacy-modes/mode/scheme";
import { commonLisp } from "@codemirror/legacy-modes/mode/commonlisp";
import { oCaml } from "@codemirror/legacy-modes/mode/mllike";
import { protobuf } from "@codemirror/legacy-modes/mode/protobuf";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { xml } from "@codemirror/legacy-modes/mode/xml";
import { standardSQL } from "@codemirror/legacy-modes/mode/sql";
import { sCSS, less } from "@codemirror/legacy-modes/mode/css";
import { toml } from "@codemirror/legacy-modes/mode/toml";
import { perl } from "@codemirror/legacy-modes/mode/perl";
import { powerShell } from "@codemirror/legacy-modes/mode/powershell";
import { haskell } from "@codemirror/legacy-modes/mode/haskell";
import { erlang } from "@codemirror/legacy-modes/mode/erlang";
import { elm } from "@codemirror/legacy-modes/mode/elm";
import { groovy } from "@codemirror/legacy-modes/mode/groovy";
import { diff } from "@codemirror/legacy-modes/mode/diff";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { cmake } from "@codemirror/legacy-modes/mode/cmake";
import { tcl } from "@codemirror/legacy-modes/mode/tcl";
import { crystal } from "@codemirror/legacy-modes/mode/crystal";
import { d } from "@codemirror/legacy-modes/mode/d";
import { fortran } from "@codemirror/legacy-modes/mode/fortran";
import { pascal } from "@codemirror/legacy-modes/mode/pascal";
import { Facet, type Extension } from "@codemirror/state";
import { wordStreamParser, type WordLangSpec } from "./codemirror-word-lang.ts";
import { indentFoldService } from "./indent-fold.ts";
import { formatSentenceCase } from "../../../format/format-sentence-case.ts";
import {
  BRACKET,
  BRACKET_BLOCKS,
  BRACKET_STATEMENTS,
  CHAIN,
  CLAUSE,
  CONTAINERS,
  DATA,
  DATA_RECORDS,
  DATA_WITH_COMMENTS,
  FENCED,
  MARKUP,
  NO_LAYOUT,
  type PrettyLayout,
  SCRIPT,
  SCRIPT_JSX,
  SEXP,
  SEXP_DATA,
  STYLE,
  STYLE_WITH_LINE_COMMENTS,
} from "../pretty/pretty-outcome.ts";

// Languages without bundled grammars use reference keyword lists and a shared tokenizer.
const WORD_LANG_SPECS = {
  php: {
    name: "php",
    keywords: [
      "abstract", "and", "array", "as", "break", "callable", "case", "catch",
      "class", "clone", "const", "continue", "declare", "default", "do", "echo",
      "else", "elseif", "empty", "enddeclare", "endfor", "endforeach", "endif",
      "endswitch", "endwhile", "enum", "extends", "final", "finally", "fn",
      "for", "foreach", "function", "global", "goto", "if", "implements",
      "include", "include_once", "instanceof", "insteadof", "interface",
      "isset", "list", "match", "namespace", "new", "or", "print", "private",
      "protected", "public", "readonly", "require", "require_once", "return",
      "static", "switch", "throw", "trait", "try", "unset", "use", "var",
      "while", "xor", "yield",
    ],
    types: [
      "int", "float", "bool", "string", "void", "mixed", "object", "iterable",
      "never", "self", "parent",
    ],
    atoms: ["true", "false", "null", "this"],
    dollarVars: true,
    atMeta: true,
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
    metaPatterns: [/^<\?(?:php|=)?/, /^\?>/],
    caseInsensitive: true,
  },
  hack: {
    name: "hack",
    keywords: [
      "abstract", "arraykey", "as", "async", "await", "break", "case", "catch",
      "class", "clone", "concurrent", "const", "continue", "default", "do",
      "echo", "else", "elseif", "enum", "extends", "final", "finally", "for",
      "foreach", "function", "if", "implements", "include", "instanceof",
      "interface", "is", "namespace", "new", "newtype", "print", "private",
      "protected", "public", "require", "return", "shape", "static", "switch",
      "throw", "trait", "try", "tuple", "type", "use", "while", "yield",
    ],
    types: [
      "int", "float", "bool", "string", "void", "mixed", "dynamic", "num",
      "nonnull", "vec", "dict", "keyset", "Vector", "Map", "Set", "Awaitable",
    ],
    atoms: ["true", "false", "null", "this"],
    dollarVars: true,
    atMeta: true,
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
  },
  elixir: {
    name: "elixir",
    keywords: [
      "def", "defp", "defmodule", "defmacro", "defmacrop", "defstruct",
      "defprotocol", "defimpl", "defdelegate", "defguard", "defguardp",
      "defexception", "do", "end", "fn", "case", "cond", "if", "unless",
      "else", "after", "rescue", "catch", "try", "receive", "for", "when",
      "in", "and", "or", "not", "raise", "throw", "import", "require",
      "alias", "use", "quote", "unquote", "with", "super",
    ],
    atoms: ["true", "false", "nil", "self"],
    colonAtoms: true,
    atMeta: true,
    capitalizedTypes: true,
    lineComments: ["#"],
  },
  hcl: {
    name: "hcl",
    keywords: [
      "resource", "data", "variable", "output", "module", "provider",
      "terraform", "locals", "dynamic", "content", "lifecycle", "backend",
      "provisioner", "connection", "moved", "import", "check", "removed",
      "for", "in", "if", "else", "endif", "endfor",
    ],
    atoms: [
      "true", "false", "null", "var", "local", "each", "count", "self",
      "path",
    ],
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
  },
  solidity: {
    name: "solidity",
    keywords: [
      "pragma", "solidity", "contract", "interface", "library", "function",
      "modifier", "constructor", "event", "error", "emit", "revert",
      "require", "assert", "mapping", "struct", "enum", "public", "private",
      "internal", "external", "pure", "view", "payable", "storage", "memory",
      "calldata", "returns", "return", "if", "else", "for", "while", "do",
      "break", "continue", "new", "delete", "import", "is", "using", "try",
      "catch", "abstract", "override", "virtual", "immutable", "constant",
      "unchecked", "assembly", "type", "indexed", "anonymous", "fallback",
      "receive",
    ],
    types: ["address", "bool", "string", "bytes", "byte", "int", "uint"],
    typePattern: /^(?:u?int|bytes)\d+$/,
    atoms: [
      "true", "false", "wei", "gwei", "ether", "seconds", "minutes", "hours",
      "days", "weeks", "msg", "block", "tx", "this", "super", "now",
    ],
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  move: {
    name: "move",
    keywords: [
      "module", "script", "fun", "public", "entry", "native", "struct", "has",
      "copy", "drop", "store", "key", "use", "as", "friend", "const", "let",
      "mut", "return", "abort", "if", "else", "while", "loop", "break",
      "continue", "spec", "schema", "invariant", "assume", "assert", "move",
      "acquires", "phantom",
    ],
    types: [
      "u8", "u16", "u32", "u64", "u128", "u256", "bool", "address", "vector",
      "signer",
    ],
    atoms: ["true", "false"],
    atMeta: true,
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  cairo: {
    name: "cairo",
    keywords: [
      "use", "mod", "fn", "struct", "enum", "trait", "impl", "of", "let",
      "mut", "ref", "return", "if", "else", "match", "loop", "while", "for",
      "break", "continue", "const", "static", "type", "as", "pub", "extern",
      "nopanic", "implicits",
    ],
    types: [
      "felt252", "usize", "bool", "u8", "u16", "u32", "u64", "u128", "u256",
      "i8", "i16", "i32", "i64", "i128", "ContractAddress",
    ],
    atoms: ["true", "false", "Some", "None", "Ok", "Err"],
    lineComments: ["//"],
  },
  circom: {
    name: "circom",
    keywords: [
      "pragma", "circom", "include", "template", "component", "signal",
      "input", "output", "public", "var", "function", "return", "if", "else",
      "for", "while", "do", "assert", "log", "parallel", "custom", "main",
    ],
    atoms: ["true", "false"],
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  apex: {
    name: "apex",
    keywords: [
      "public", "private", "protected", "global", "static", "final",
      "abstract", "virtual", "override", "class", "interface", "extends",
      "implements", "trigger", "on", "new", "return", "if", "else", "for",
      "while", "do", "break", "continue", "try", "catch", "finally", "throw",
      "insert", "update", "upsert", "delete", "undelete", "merge",
      "testmethod", "webservice", "with", "without", "sharing", "inherited",
      "get", "set", "enum", "switch", "when", "select", "from", "where",
      "limit", "order", "by",
    ],
    types: [
      "void", "String", "Integer", "Boolean", "Decimal", "Double", "Long",
      "Date", "Datetime", "Time", "Id", "List", "Set", "Map", "Object",
      "SObject", "Blob",
    ],
    atoms: ["true", "false", "null", "this", "super"],
    atMeta: true,
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
    caseInsensitive: true,
  },
  ql: {
    name: "ql",
    keywords: [
      "import", "module", "class", "extends", "implements", "predicate",
      "query", "from", "where", "select", "as", "order", "by", "asc", "desc",
      "exists", "forall", "forex", "not", "and", "or", "implies", "if",
      "then", "else", "instanceof", "in", "newtype", "cached", "abstract",
      "final", "private", "deprecated", "pragma", "language", "bindingset",
      "signature", "default", "external", "transient",
    ],
    types: ["int", "float", "string", "boolean", "date"],
    atoms: ["true", "false", "none", "this", "result", "super", "any"],
    atMeta: true,
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  jsonnet: {
    name: "jsonnet",
    keywords: [
      "local", "function", "if", "then", "else", "for", "in", "import",
      "importstr", "importbin", "error", "assert", "tailstrict",
    ],
    atoms: ["true", "false", "null", "self", "super"],
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
  },
  promql: {
    name: "promql",
    keywords: [
      "by", "without", "on", "ignoring", "group_left", "group_right",
      "offset", "bool", "and", "or", "unless", "atan2", "start", "end",
      "sum", "avg", "min", "max", "count", "count_values", "stddev", "stdvar",
      "topk", "bottomk", "quantile", "rate", "irate", "increase", "delta",
      "idelta", "histogram_quantile", "label_replace", "label_join", "vector",
      "time", "absent", "absent_over_time", "ceil", "floor", "round", "clamp",
      "clamp_min", "clamp_max", "exp", "ln", "log2", "log10", "sqrt", "abs",
      "changes", "resets", "predict_linear", "deriv", "avg_over_time",
      "min_over_time", "max_over_time", "sum_over_time", "count_over_time",
      "quantile_over_time", "last_over_time", "present_over_time", "sort",
      "sort_desc", "timestamp", "scalar",
    ],
    atoms: ["Inf", "NaN"],
    lineComments: ["#"],
  },
} satisfies Record<string, WordLangSpec>;

/** Formatters whose configuration sets a language's line width, in priority order. */
const WEB_FORMATTERS = ["prettier", "biome", "dprint"] as const satisfies readonly PrettyFormatTool[];
const MARKUP_FORMATTERS = ["prettier", "dprint"] as const satisfies readonly PrettyFormatTool[];
const PYTHON_FORMATTERS = ["ruff", "black"] as const satisfies readonly PrettyFormatTool[];
const GO_FORMATTERS = ["golangci"] as const satisfies readonly PrettyFormatTool[];
const RUST_FORMATTERS = ["rustfmt"] as const satisfies readonly PrettyFormatTool[];
const CLANG_FORMATTERS = ["clang_format"] as const satisfies readonly PrettyFormatTool[];
const JAVA_FORMATTERS = ["checkstyle", "clang_format"] as const satisfies readonly PrettyFormatTool[];
const SCALA_FORMATTERS = ["scalafmt"] as const satisfies readonly PrettyFormatTool[];
const SWIFT_FORMATTERS = ["swift_format"] as const satisfies readonly PrettyFormatTool[];
const DART_FORMATTERS = ["dart"] as const satisfies readonly PrettyFormatTool[];
const RUBY_FORMATTERS = ["rubocop"] as const satisfies readonly PrettyFormatTool[];
const PHP_FORMATTERS = ["pint", "phpcs", "prettier"] as const satisfies readonly PrettyFormatTool[];
const ELIXIR_FORMATTERS = ["mix_format"] as const satisfies readonly PrettyFormatTool[];
const CLOJURE_FORMATTERS = ["cljfmt"] as const satisfies readonly PrettyFormatTool[];
const LUA_FORMATTERS = ["stylua"] as const satisfies readonly PrettyFormatTool[];
const OCAML_FORMATTERS = ["ocamlformat"] as const satisfies readonly PrettyFormatTool[];
const HASKELL_FORMATTERS = ["fourmolu"] as const satisfies readonly PrettyFormatTool[];
const JULIA_FORMATTERS = ["julia_formatter"] as const satisfies readonly PrettyFormatTool[];
const SQL_FORMATTERS = ["sqlfluff"] as const satisfies readonly PrettyFormatTool[];
const SOLIDITY_FORMATTERS = ["foundry", "prettier"] as const satisfies readonly PrettyFormatTool[];
const TOML_FORMATTERS = ["taplo", "dprint"] as const satisfies readonly PrettyFormatTool[];
const NO_FORMATTERS = [] as const satisfies readonly PrettyFormatTool[];

export const PLAIN_TEXT_LABEL = "Plain text";

/** Each catalog entry: its grammar (null for plain text), its label, how Pretty lays it out, and its formatters. */
interface CatalogEntry {
  readonly grammar: (() => LanguageSupport | Language) | null;
  readonly label: string;
  readonly pretty: PrettyLayout;
  readonly formatters: readonly PrettyFormatTool[];
}

const CATALOG = {
  ts: { grammar: () => javascript({ typescript: true }), label: "TypeScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  tsx: { grammar: () => javascript({ typescript: true, jsx: true }), label: "TSX", pretty: SCRIPT_JSX, formatters: WEB_FORMATTERS },
  js: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  jsx: { grammar: () => javascript({ jsx: true }), label: "JSX", pretty: SCRIPT_JSX, formatters: WEB_FORMATTERS },
  mjs: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  cjs: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  mts: { grammar: () => javascript({ typescript: true }), label: "TypeScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  cts: { grammar: () => javascript({ typescript: true }), label: "TypeScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  go: { grammar: () => go(), label: "Go", pretty: BRACKET_BLOCKS, formatters: GO_FORMATTERS },
  rs: { grammar: () => rust(), label: "Rust", pretty: BRACKET_STATEMENTS, formatters: RUST_FORMATTERS },
  py: { grammar: () => python(), label: "Python", pretty: BRACKET, formatters: PYTHON_FORMATTERS },
  pyi: { grammar: () => python(), label: "Python", pretty: BRACKET, formatters: PYTHON_FORMATTERS },
  json: { grammar: () => json(), label: "JSON", pretty: DATA, formatters: WEB_FORMATTERS },
  jsonc: { grammar: () => json(), label: "JSON", pretty: DATA_WITH_COMMENTS, formatters: WEB_FORMATTERS },
  json5: { grammar: () => json(), label: "JSON5", pretty: DATA_WITH_COMMENTS, formatters: WEB_FORMATTERS },
  jsonl: { grammar: () => json(), label: "JSON Lines", pretty: DATA_RECORDS, formatters: NO_FORMATTERS },
  ndjson: { grammar: () => json(), label: "NDJSON", pretty: DATA_RECORDS, formatters: NO_FORMATTERS },
  webmanifest: { grammar: () => json(), label: "Web app manifest", pretty: DATA, formatters: WEB_FORMATTERS },
  har: { grammar: () => json(), label: "HAR", pretty: DATA, formatters: WEB_FORMATTERS },
  avsc: { grammar: () => json(), label: "Avro schema", pretty: DATA, formatters: WEB_FORMATTERS },
  geojson: { grammar: () => json(), label: "GeoJSON", pretty: DATA, formatters: WEB_FORMATTERS },
  topojson: { grammar: () => json(), label: "TopoJSON", pretty: DATA, formatters: WEB_FORMATTERS },
  sarif: { grammar: () => json(), label: "SARIF", pretty: DATA, formatters: WEB_FORMATTERS },
  tfstate: { grammar: () => json(), label: "Terraform state", pretty: DATA, formatters: NO_FORMATTERS },
  yaml: { grammar: () => yaml(), label: "YAML", pretty: BRACKET, formatters: MARKUP_FORMATTERS },
  yml: { grammar: () => yaml(), label: "YAML", pretty: BRACKET, formatters: MARKUP_FORMATTERS },
  md: { grammar: () => markdown({ codeLanguages: languageForInfoString }), label: "Markdown", pretty: FENCED, formatters: MARKUP_FORMATTERS },
  mdx: { grammar: () => markdown({ codeLanguages: languageForInfoString }), label: "Markdown", pretty: FENCED, formatters: MARKUP_FORMATTERS },
  sh: { grammar: () => defineStream(shell), label: "Shell", pretty: CHAIN, formatters: NO_FORMATTERS },
  bash: { grammar: () => defineStream(shell), label: "Shell", pretty: CHAIN, formatters: NO_FORMATTERS },
  zsh: { grammar: () => defineStream(shell), label: "Shell", pretty: CHAIN, formatters: NO_FORMATTERS },
  c: { grammar: () => defineStream(c), label: "C", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  h: { grammar: () => defineStream(c), label: "C", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cc: { grammar: () => defineStream(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cpp: { grammar: () => defineStream(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cxx: { grammar: () => defineStream(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  hpp: { grammar: () => defineStream(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  hh: { grammar: () => defineStream(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  hxx: { grammar: () => defineStream(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  java: { grammar: () => defineStream(java), label: "Java", pretty: BRACKET_STATEMENTS, formatters: JAVA_FORMATTERS },
  cs: { grammar: () => defineStream(csharp), label: "C#", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  csx: { grammar: () => defineStream(csharp), label: "C#", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cake: { grammar: () => defineStream(csharp), label: "C#", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  scala: { grammar: () => defineStream(scala), label: "Scala", pretty: BRACKET_STATEMENTS, formatters: SCALA_FORMATTERS },
  kt: { grammar: () => defineStream(kotlin), label: "Kotlin", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  kts: { grammar: () => defineStream(kotlin), label: "Kotlin", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  rb: { grammar: () => defineStream(ruby), label: "Ruby", pretty: BRACKET, formatters: RUBY_FORMATTERS },
  swift: { grammar: () => defineStream(swift), label: "Swift", pretty: BRACKET_STATEMENTS, formatters: SWIFT_FORMATTERS },
  lua: { grammar: () => defineStream(lua), label: "Lua", pretty: BRACKET, formatters: LUA_FORMATTERS },
  r: { grammar: () => defineStream(r), label: "R", pretty: BRACKET, formatters: NO_FORMATTERS },
  jl: { grammar: () => defineStream(julia), label: "Julia", pretty: BRACKET, formatters: JULIA_FORMATTERS },
  clj: { grammar: () => defineStream(clojure), label: "Clojure", pretty: SEXP, formatters: CLOJURE_FORMATTERS },
  cljs: { grammar: () => defineStream(clojure), label: "Clojure", pretty: SEXP, formatters: CLOJURE_FORMATTERS },
  cljc: { grammar: () => defineStream(clojure), label: "Clojure", pretty: SEXP, formatters: CLOJURE_FORMATTERS },
  edn: { grammar: () => defineStream(clojure), label: "Clojure", pretty: SEXP_DATA, formatters: CLOJURE_FORMATTERS },
  scm: { grammar: () => defineStream(scheme), label: "Scheme", pretty: SEXP, formatters: NO_FORMATTERS },
  ss: { grammar: () => defineStream(scheme), label: "Scheme", pretty: SEXP, formatters: NO_FORMATTERS },
  lisp: { grammar: () => defineStream(commonLisp), label: "Common Lisp", pretty: SEXP, formatters: NO_FORMATTERS },
  cl: { grammar: () => defineStream(commonLisp), label: "Common Lisp", pretty: SEXP, formatters: NO_FORMATTERS },
  lsp: { grammar: () => defineStream(commonLisp), label: "Common Lisp", pretty: SEXP, formatters: NO_FORMATTERS },
  ml: { grammar: () => defineStream(oCaml), label: "OCaml", pretty: BRACKET, formatters: OCAML_FORMATTERS },
  mli: { grammar: () => defineStream(oCaml), label: "OCaml", pretty: BRACKET, formatters: OCAML_FORMATTERS },
  proto: { grammar: () => defineStream(protobuf), label: "Protobuf", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  dockerfile: { grammar: () => defineStream(dockerFile), label: "Dockerfile", pretty: CHAIN, formatters: NO_FORMATTERS },
  xml: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  svg: { grammar: () => defineStream(xml), label: "SVG", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  csproj: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  vbproj: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  fsproj: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  vcxproj: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  props: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  targets: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  nuspec: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  xaml: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  plist: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  xsd: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  wsdl: { grammar: () => defineStream(xml), label: "XML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  // The HTML parser applies embedded JavaScript and CSS grammars to component files.
  html: { grammar: () => html(), label: "HTML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  htm: { grammar: () => html(), label: "HTML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  vue: { grammar: () => html(), label: "Vue", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  svelte: { grammar: () => html(), label: "Svelte", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  astro: { grammar: () => html(), label: "Astro", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  sql: { grammar: () => defineStream(standardSQL), label: "SQL", pretty: CLAUSE, formatters: SQL_FORMATTERS },
  css: { grammar: () => cssLang(), label: "CSS", pretty: STYLE, formatters: WEB_FORMATTERS },
  // SCSS and Less nest rules and carry their own directives; `lang-css` parses
  // plain CSS only, so the preprocessor dialects stay on the stream modes.
  scss: { grammar: () => defineStream(sCSS), label: "SCSS", pretty: STYLE_WITH_LINE_COMMENTS, formatters: WEB_FORMATTERS },
  less: { grammar: () => defineStream(less), label: "Less", pretty: STYLE_WITH_LINE_COMMENTS, formatters: WEB_FORMATTERS },
  toml: { grammar: () => defineStream(toml), label: "TOML", pretty: BRACKET, formatters: TOML_FORMATTERS },
  al: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  perl: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  ph: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  pl: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  plx: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  pm: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  psgi: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  t: { grammar: () => defineStream(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  ps1: { grammar: () => defineStream(powerShell), label: "PowerShell", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  psm1: { grammar: () => defineStream(powerShell), label: "PowerShell", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  psd1: { grammar: () => defineStream(powerShell), label: "PowerShell", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  hs: { grammar: () => defineStream(haskell), label: "Haskell", pretty: BRACKET, formatters: HASKELL_FORMATTERS },
  erl: { grammar: () => defineStream(erlang), label: "Erlang", pretty: BRACKET, formatters: NO_FORMATTERS },
  hrl: { grammar: () => defineStream(erlang), label: "Erlang", pretty: BRACKET, formatters: NO_FORMATTERS },
  elm: { grammar: () => defineStream(elm), label: "Elm", pretty: BRACKET, formatters: NO_FORMATTERS },
  groovy: { grammar: () => defineStream(groovy), label: "Groovy", pretty: BRACKET_STATEMENTS, formatters: JAVA_FORMATTERS },
  gradle: { grammar: () => defineStream(groovy), label: "Gradle", pretty: BRACKET_STATEMENTS, formatters: JAVA_FORMATTERS },
  gvy: { grammar: () => defineStream(groovy), label: "Groovy", pretty: BRACKET_STATEMENTS, formatters: JAVA_FORMATTERS },
  diff: { grammar: () => defineStream(diff), label: "Diff", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  patch: { grammar: () => defineStream(diff), label: "Diff", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  properties: { grammar: () => defineStream(properties), label: "Properties", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  ini: { grammar: () => defineStream(properties), label: "INI", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  env: { grammar: () => defineStream(properties), label: "Dotenv", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cfg: { grammar: () => defineStream(properties), label: "Config", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cmake: { grammar: () => defineStream(cmake), label: "CMake", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  tcl: { grammar: () => defineStream(tcl), label: "Tcl", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cr: { grammar: () => defineStream(crystal), label: "Crystal", pretty: BRACKET, formatters: NO_FORMATTERS },
  d: { grammar: () => defineStream(d), label: "D", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  f: { grammar: () => defineStream(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  f90: { grammar: () => defineStream(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  f95: { grammar: () => defineStream(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  f03: { grammar: () => defineStream(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  pas: { grammar: () => defineStream(pascal), label: "Pascal", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  dart: { grammar: () => defineStream(dart), label: "Dart", pretty: BRACKET_STATEMENTS, formatters: DART_FORMATTERS },
  sass: { grammar: () => defineStream(sass), label: "Sass", pretty: BRACKET, formatters: NO_FORMATTERS },
  php: { grammar: () => defineWord(WORD_LANG_SPECS.php), label: "PHP", pretty: BRACKET_STATEMENTS, formatters: PHP_FORMATTERS },
  hack: { grammar: () => defineWord(WORD_LANG_SPECS.hack), label: "Hack", pretty: BRACKET_STATEMENTS, formatters: PHP_FORMATTERS },
  ex: { grammar: () => defineWord(WORD_LANG_SPECS.elixir), label: "Elixir", pretty: BRACKET, formatters: ELIXIR_FORMATTERS },
  exs: { grammar: () => defineWord(WORD_LANG_SPECS.elixir), label: "Elixir", pretty: BRACKET, formatters: ELIXIR_FORMATTERS },
  hcl: { grammar: () => defineWord(WORD_LANG_SPECS.hcl), label: "HCL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  tf: { grammar: () => defineWord(WORD_LANG_SPECS.hcl), label: "Terraform", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  tfvars: { grammar: () => defineWord(WORD_LANG_SPECS.hcl), label: "Terraform", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  tofu: { grammar: () => defineWord(WORD_LANG_SPECS.hcl), label: "OpenTofu", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  nomad: { grammar: () => defineWord(WORD_LANG_SPECS.hcl), label: "HCL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  bzl: { grammar: () => python(), label: "Starlark", pretty: BRACKET, formatters: NO_FORMATTERS },
  sol: { grammar: () => defineWord(WORD_LANG_SPECS.solidity), label: "Solidity", pretty: BRACKET_STATEMENTS, formatters: SOLIDITY_FORMATTERS },
  move: { grammar: () => defineWord(WORD_LANG_SPECS.move), label: "Move", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  cairo: { grammar: () => defineWord(WORD_LANG_SPECS.cairo), label: "Cairo", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  circom: { grammar: () => defineWord(WORD_LANG_SPECS.circom), label: "Circom", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  cls: { grammar: () => defineWord(WORD_LANG_SPECS.apex), label: "Apex", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  trigger: { grammar: () => defineWord(WORD_LANG_SPECS.apex), label: "Apex", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  ql: { grammar: () => defineWord(WORD_LANG_SPECS.ql), label: "CodeQL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  qll: { grammar: () => defineWord(WORD_LANG_SPECS.ql), label: "CodeQL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  jsonnet: { grammar: () => defineWord(WORD_LANG_SPECS.jsonnet), label: "Jsonnet", pretty: DATA_WITH_COMMENTS, formatters: NO_FORMATTERS },
  libsonnet: { grammar: () => defineWord(WORD_LANG_SPECS.jsonnet), label: "Jsonnet", pretty: DATA_WITH_COMMENTS, formatters: NO_FORMATTERS },
  promql: { grammar: () => defineWord(WORD_LANG_SPECS.promql), label: "PromQL", pretty: BRACKET, formatters: NO_FORMATTERS },
  log: { grammar: null, label: "Log", pretty: CONTAINERS, formatters: NO_FORMATTERS },
  out: { grammar: null, label: "Log", pretty: CONTAINERS, formatters: NO_FORMATTERS },
  txt: { grammar: null, label: PLAIN_TEXT_LABEL, pretty: CONTAINERS, formatters: NO_FORMATTERS },
} satisfies Record<string, CatalogEntry>;

type CatalogKey = keyof typeof CATALOG;

function catalogEntry(ext: string): CatalogEntry | undefined {
  return Object.hasOwn(CATALOG, ext) ? CATALOG[ext as CatalogKey] : undefined;
}

interface BasenameSpec {
  readonly ext?: CatalogKey;
  readonly label: string;
}

/** Well-known extensionless, dot-prefixed, or extension-ambiguous filenames. */
const BASENAMES: Record<string, BasenameSpec> = {
  dockerfile: { ext: "dockerfile", label: "Dockerfile" },
  containerfile: { ext: "dockerfile", label: "Dockerfile" },
  "cmakelists.txt": { ext: "cmake", label: "CMake" },
  ".env": { ext: "env", label: "Dotenv" },
  ".gitignore": { ext: "properties", label: "Git ignore" },
  ".gitattributes": { ext: "properties", label: "Git attributes" },
  ".gitmodules": { ext: "properties", label: "Git modules" },
  ".gitconfig": { ext: "properties", label: "Git config" },
  ".editorconfig": { ext: "properties", label: "EditorConfig" },
  ".npmrc": { ext: "properties", label: "npm config" },
  ".clang-format": { ext: "yaml", label: "YAML" },
  ".clang-tidy": { ext: "yaml", label: "YAML" },
  ".clangd": { ext: "yaml", label: "YAML" },
  gemfile: { ext: "rb", label: "Ruby" },
  rakefile: { ext: "rb", label: "Ruby" },
  vagrantfile: { ext: "rb", label: "Ruby" },
  podfile: { ext: "rb", label: "Ruby" },
  fastfile: { ext: "rb", label: "Ruby" },
  brewfile: { ext: "rb", label: "Ruby" },
  jenkinsfile: { ext: "groovy", label: "Groovy" },
  build: { ext: "bzl", label: "Starlark" },
  "build.bazel": { ext: "bzl", label: "Starlark" },
  workspace: { ext: "bzl", label: "Starlark" },
  "workspace.bazel": { ext: "bzl", label: "Starlark" },
  ".latexmkrc": { ext: "pl", label: "Perl" },
  ack: { ext: "pl", label: "Perl" },
  cpanfile: { ext: "pl", label: "Perl" },
  latexmkrc: { ext: "pl", label: "Perl" },
  rexfile: { ext: "pl", label: "Perl" },
  "tsconfig.json": { ext: "jsonc", label: "JSON" },
  "jsconfig.json": { ext: "jsonc", label: "JSON" },
  "devcontainer.json": { ext: "jsonc", label: "JSON" },
  ".devcontainer.json": { ext: "jsonc", label: "JSON" },
  "composer.lock": { ext: "json", label: "JSON" },
  "flake.lock": { ext: "json", label: "JSON" },
  "deno.lock": { ext: "json", label: "JSON" },
  "pipfile.lock": { ext: "json", label: "JSON" },
  "package.resolved": { ext: "json", label: "JSON" },
  "cargo.lock": { ext: "toml", label: "TOML" },
  "poetry.lock": { ext: "toml", label: "TOML" },
  "uv.lock": { ext: "toml", label: "TOML" },
  "pdm.lock": { ext: "toml", label: "TOML" },
  pipfile: { ext: "toml", label: "TOML" },
  "gopkg.lock": { ext: "toml", label: "TOML" },
  makefile: { label: "Makefile" },
  "go.mod": { label: "Go module" },
  "go.sum": { label: "Go checksums" },
};

const DETECTED_LANGUAGE_TO_LABEL: Record<string, string> = {
  c_sharp: "C#",
  commonlisp: "Common Lisp",
  gomod: "Go module",
  javascript: "JavaScript",
  json5: "JSON5",
  markdown: "Markdown",
  typescript: "TypeScript",
  vimdoc: "Vim help",
};

const DETECTED_LANGUAGE_TO_EXT: Record<string, CatalogKey> = {
  c_sharp: "cs",
  commonlisp: "lisp",
  csharp: "cs",
  golang: "go",
  py3: "py",
  python3: "py",
  postgres: "sql",
  postgresql: "sql",
  mysql: "sql",
  sqlite: "sql",
  shell: "sh",
  console: "sh",
  jsonlines: "jsonl",
  tf: "tf",
  opentofu: "tofu",
};

export function languageLabelForPath(path: string): string {
  const base = basenameOf(path);
  const ext = extensionOf(base);
  const byName = BASENAMES[base];
  if (byName) return byName.label;
  if (isCommentedJsonConfig(base)) return "JSON";
  const entry = ext ? catalogEntry(ext) : undefined;
  if (entry) return entry.label;
  return ext || "text";
}

/** Human-facing label for a host-detected grammar, with a path fallback. */
export function languageLabelForDetectedLanguage(
  language: string,
  path: string,
): string {
  const key = language.trim().toLowerCase();
  if (!key) return languageLabelForPath(path);
  if (key === "vimdoc" && extensionOf(basenameOf(path)) === "txt") {
    return PLAIN_TEXT_LABEL;
  }
  const known = DETECTED_LANGUAGE_TO_LABEL[key] ?? catalogEntry(key)?.label;
  if (known) return known;
  return formatSentenceCase(key);
}

/** Maps fenced-code labels to the viewer's language grammars. */
const INFO_TO_EXT: ReadonlyMap<string, CatalogKey> = (() => {
  const map = new Map<string, CatalogKey>();
  for (const ext of Object.keys(CATALOG) as CatalogKey[]) map.set(ext, ext);
  for (const [ext, entry] of Object.entries(CATALOG) as [CatalogKey, CatalogEntry][]) {
    const key = entry.label.toLowerCase();
    if (!map.has(key) && entry.grammar) map.set(key, ext);
  }
  for (const [alias, ext] of Object.entries(DETECTED_LANGUAGE_TO_EXT)) {
    if (!map.has(alias)) map.set(alias, ext);
  }
  return map;
})();

function normalizeLanguageToken(name: string): CatalogKey | undefined {
  const trimmed = name.trim().toLowerCase().replace(/^\./, "");
  if (!trimmed) return undefined;
  const exact = INFO_TO_EXT.get(trimmed);
  if (exact) return exact;
  const first = (trimmed.split(/\s+/, 1)[0] ?? "").replace(/^\./, "");
  return first ? INFO_TO_EXT.get(first) : undefined;
}

/** The `Language` a fence's info string names, or null to leave it plain. */
export function languageForInfoString(info: string): Language | null {
  const ext = normalizeLanguageToken(info);
  const factory = ext ? catalogEntry(ext)?.grammar : undefined;
  if (!factory) return null;
  const value = factory();
  return value instanceof LanguageSupport ? value.language : value;
}

export const prettyStreamParserFacet = Facet.define<StreamParser<unknown> | null, StreamParser<unknown> | null>({
  combine: (values) => values.find((v) => v != null) ?? null,
});

const STREAM_PARSERS = new WeakMap<Language, StreamParser<unknown>>();

function defineStream<S>(parser: StreamParser<S>): StreamLanguage<S> {
  const lang = defineStream(parser);
  STREAM_PARSERS.set(lang, parser as StreamParser<unknown>);
  return lang;
}

function defineWord(spec: WordLangSpec): StreamLanguage<unknown> {
  return defineStream(wordStreamParser(spec));
}

function extensionForEntry(entry: CatalogEntry | undefined): Extension | null {
  const factory = entry?.grammar;
  if (!factory) return null;
  const value = factory();
  // Stream languages use indentation because they provide no syntax fold ranges.
  if (value instanceof StreamLanguage) {
    const parser = STREAM_PARSERS.get(value) ?? null;
    return [value, indentFoldService, ...(parser ? [prettyStreamParserFacet.of(parser)] : [])];
  }
  return value;
}

export function languageExtensionForPath(path: string): Extension | null {
  return extensionForEntry(catalogEntryForPath(path));
}

export function languageExtensionForBuffer(path: string, detectedLanguage?: string): Extension | null {
  return extensionForEntry(catalogEntryForBuffer(path, detectedLanguage));
}

function isCommentedJsonConfig(base: string): boolean {
  return (
    (base.startsWith("tsconfig.") || base.startsWith("jsconfig.")) &&
    base.endsWith(".json")
  );
}

function catalogEntryForPath(path: string): CatalogEntry | undefined {
  const base = basenameOf(path);
  const ext = extensionOf(base);
  // Well-known names win over their extension, as `CMakeLists.txt` does over `.txt`.
  const named = BASENAMES[base]?.ext;
  if (named) return catalogEntry(named);
  if (isCommentedJsonConfig(base)) return CATALOG.jsonc;
  return ext ? catalogEntry(ext) : undefined;
}

function catalogEntryForBuffer(path: string, detectedLanguage?: string): CatalogEntry | undefined {
  const byPath = catalogEntryForPath(path);
  if (byPath && byPath !== CATALOG.txt) return byPath;
  if (detectedLanguage) {
    const ext = normalizeLanguageToken(detectedLanguage);
    const detected = ext ? catalogEntry(ext) : undefined;
    if (detected && detected !== CATALOG.txt) return detected;
  }
  return byPath;
}

/** Whether `path` maps to an explicit catalog entry (including NO_LAYOUT formats). */
export function hasCatalogEntryForPath(path: string): boolean {
  return catalogEntryForPath(path) !== undefined;
}

/** How Pretty lays out a file; a file outside the catalog has no layout. */
export function prettyLayoutForPath(path: string): PrettyLayout {
  return catalogEntryForPath(path)?.pretty ?? NO_LAYOUT;
}

export function prettyLayoutForBuffer(path: string, detectedLanguage?: string): PrettyLayout {
  return catalogEntryForBuffer(path, detectedLanguage)?.pretty ?? NO_LAYOUT;
}

/** Formatters whose configuration can set the file's line width, in priority order. */
export function formattersForPath(path: string): readonly PrettyFormatTool[] {
  return catalogEntryForPath(path)?.formatters ?? NO_FORMATTERS;
}

export function formattersForBuffer(path: string, detectedLanguage?: string): readonly PrettyFormatTool[] {
  return catalogEntryForBuffer(path, detectedLanguage)?.formatters ?? NO_FORMATTERS;
}

/** How Pretty lays out text named by a language, label or fence info string. */
export function prettyLayoutForLanguage(name: string): PrettyLayout {
  const ext = normalizeLanguageToken(name);
  return (ext ? catalogEntry(ext)?.pretty : undefined) ?? NO_LAYOUT;
}

/** Every catalog extension with its label and Pretty layout. */
export function prettyLayoutCatalog(): ReadonlyArray<{ readonly ext: string; readonly label: string; readonly pretty: PrettyLayout }> {
  return (Object.entries(CATALOG) as [string, CatalogEntry][]).map(([ext, entry]) => ({ ext, label: entry.label, pretty: entry.pretty }));
}

function basenameOf(path: string): string {
  return (
    path.trim().replace(/\\/g, "/").split("/").pop() ?? ""
  ).toLowerCase();
}

function extensionOf(basename: string): string {
  const i = basename.lastIndexOf(".");
  if (i <= 0 || i === basename.length - 1) return "";
  return basename.slice(i + 1);
}
