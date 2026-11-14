import type { FormatTool } from "../../../api/types.ts";
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
import type { Extension } from "@codemirror/state";
import { wordLanguage, type WordLangSpec } from "./codemirror-word-lang.ts";
import { indentFoldService } from "./indent-fold.ts";
import { formatSentenceCase } from "../../../format/format-sentence-case.ts";
import {
  BRACKET,
  BRACKET_STATEMENTS,
  CLAUSE,
  DATA,
  DATA_RECORDS,
  DATA_WITH_COMMENTS,
  FENCED,
  MARKUP,
  NO_LAYOUT,
  type PrettyLayout,
  SCRIPT,
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
const WEB_FORMATTERS = ["prettier", "biome", "dprint"] as const satisfies readonly FormatTool[];
const MARKUP_FORMATTERS = ["prettier", "dprint"] as const satisfies readonly FormatTool[];
const PYTHON_FORMATTERS = ["ruff", "black"] as const satisfies readonly FormatTool[];
const RUST_FORMATTERS = ["rustfmt"] as const satisfies readonly FormatTool[];
const CLANG_FORMATTERS = ["clang_format"] as const satisfies readonly FormatTool[];
const SCALA_FORMATTERS = ["scalafmt"] as const satisfies readonly FormatTool[];
const SWIFT_FORMATTERS = ["swift_format"] as const satisfies readonly FormatTool[];
const DART_FORMATTERS = ["dart"] as const satisfies readonly FormatTool[];
const RUBY_FORMATTERS = ["rubocop"] as const satisfies readonly FormatTool[];
const LUA_FORMATTERS = ["stylua"] as const satisfies readonly FormatTool[];
const OCAML_FORMATTERS = ["ocamlformat"] as const satisfies readonly FormatTool[];
const HASKELL_FORMATTERS = ["fourmolu"] as const satisfies readonly FormatTool[];
const JULIA_FORMATTERS = ["julia_formatter"] as const satisfies readonly FormatTool[];
const SQL_FORMATTERS = ["sqlfluff"] as const satisfies readonly FormatTool[];
const SOLIDITY_FORMATTERS = ["foundry", "prettier"] as const satisfies readonly FormatTool[];
const TOML_FORMATTERS = ["taplo", "dprint"] as const satisfies readonly FormatTool[];
const NO_FORMATTERS = [] as const satisfies readonly FormatTool[];

/** Each catalog entry: its grammar (null for plain text), its label, how Pretty lays it out, and its formatters. */
interface CatalogEntry {
  readonly grammar: (() => LanguageSupport | Language) | null;
  readonly label: string;
  readonly pretty: PrettyLayout;
  readonly formatters: readonly FormatTool[];
}

const CATALOG = {
  ts: { grammar: () => javascript({ typescript: true }), label: "TypeScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  tsx: { grammar: () => javascript({ typescript: true, jsx: true }), label: "TSX", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  js: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  jsx: { grammar: () => javascript({ jsx: true }), label: "JSX", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  mjs: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  cjs: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT, formatters: WEB_FORMATTERS },
  go: { grammar: () => go(), label: "Go", pretty: BRACKET, formatters: NO_FORMATTERS },
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
  yaml: { grammar: () => yaml(), label: "YAML", pretty: BRACKET, formatters: MARKUP_FORMATTERS },
  yml: { grammar: () => yaml(), label: "YAML", pretty: BRACKET, formatters: MARKUP_FORMATTERS },
  md: { grammar: () => markdown({ codeLanguages: languageForInfoString }), label: "Markdown", pretty: FENCED, formatters: MARKUP_FORMATTERS },
  mdx: { grammar: () => markdown({ codeLanguages: languageForInfoString }), label: "Markdown", pretty: FENCED, formatters: MARKUP_FORMATTERS },
  sh: { grammar: () => StreamLanguage.define(shell), label: "Shell", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  bash: { grammar: () => StreamLanguage.define(shell), label: "Shell", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  zsh: { grammar: () => StreamLanguage.define(shell), label: "Shell", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  c: { grammar: () => StreamLanguage.define(c), label: "C", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  h: { grammar: () => StreamLanguage.define(c), label: "C", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cc: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cpp: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cxx: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  hpp: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  hh: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  hxx: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  java: { grammar: () => StreamLanguage.define(java), label: "Java", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  cs: { grammar: () => StreamLanguage.define(csharp), label: "C#", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  scala: { grammar: () => StreamLanguage.define(scala), label: "Scala", pretty: BRACKET_STATEMENTS, formatters: SCALA_FORMATTERS },
  kt: { grammar: () => StreamLanguage.define(kotlin), label: "Kotlin", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  kts: { grammar: () => StreamLanguage.define(kotlin), label: "Kotlin", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  rb: { grammar: () => StreamLanguage.define(ruby), label: "Ruby", pretty: BRACKET, formatters: RUBY_FORMATTERS },
  swift: { grammar: () => StreamLanguage.define(swift), label: "Swift", pretty: BRACKET_STATEMENTS, formatters: SWIFT_FORMATTERS },
  lua: { grammar: () => StreamLanguage.define(lua), label: "Lua", pretty: BRACKET, formatters: LUA_FORMATTERS },
  r: { grammar: () => StreamLanguage.define(r), label: "R", pretty: BRACKET, formatters: NO_FORMATTERS },
  jl: { grammar: () => StreamLanguage.define(julia), label: "Julia", pretty: BRACKET, formatters: JULIA_FORMATTERS },
  clj: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cljs: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cljc: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  edn: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  scm: { grammar: () => StreamLanguage.define(scheme), label: "Scheme", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  ss: { grammar: () => StreamLanguage.define(scheme), label: "Scheme", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  lisp: { grammar: () => StreamLanguage.define(commonLisp), label: "Common Lisp", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cl: { grammar: () => StreamLanguage.define(commonLisp), label: "Common Lisp", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  lsp: { grammar: () => StreamLanguage.define(commonLisp), label: "Common Lisp", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  ml: { grammar: () => StreamLanguage.define(oCaml), label: "OCaml", pretty: BRACKET, formatters: OCAML_FORMATTERS },
  mli: { grammar: () => StreamLanguage.define(oCaml), label: "OCaml", pretty: BRACKET, formatters: OCAML_FORMATTERS },
  proto: { grammar: () => StreamLanguage.define(protobuf), label: "Protobuf", pretty: BRACKET_STATEMENTS, formatters: CLANG_FORMATTERS },
  dockerfile: { grammar: () => StreamLanguage.define(dockerFile), label: "Dockerfile", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  xml: { grammar: () => StreamLanguage.define(xml), label: "XML", pretty: MARKUP, formatters: NO_FORMATTERS },
  svg: { grammar: () => StreamLanguage.define(xml), label: "SVG", pretty: MARKUP, formatters: NO_FORMATTERS },
  // The HTML parser applies embedded JavaScript and CSS grammars to component files.
  html: { grammar: () => html(), label: "HTML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  htm: { grammar: () => html(), label: "HTML", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  vue: { grammar: () => html(), label: "Vue", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  svelte: { grammar: () => html(), label: "Svelte", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  astro: { grammar: () => html(), label: "Astro", pretty: MARKUP, formatters: MARKUP_FORMATTERS },
  sql: { grammar: () => StreamLanguage.define(standardSQL), label: "SQL", pretty: CLAUSE, formatters: SQL_FORMATTERS },
  css: { grammar: () => cssLang(), label: "CSS", pretty: STYLE, formatters: WEB_FORMATTERS },
  // SCSS and Less nest rules and carry their own directives; `lang-css` parses
  // plain CSS only, so the preprocessor dialects stay on the stream modes.
  scss: { grammar: () => StreamLanguage.define(sCSS), label: "SCSS", pretty: STYLE_WITH_LINE_COMMENTS, formatters: WEB_FORMATTERS },
  less: { grammar: () => StreamLanguage.define(less), label: "Less", pretty: STYLE_WITH_LINE_COMMENTS, formatters: WEB_FORMATTERS },
  toml: { grammar: () => StreamLanguage.define(toml), label: "TOML", pretty: BRACKET, formatters: TOML_FORMATTERS },
  al: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  perl: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  ph: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  pl: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  plx: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  pm: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  psgi: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  t: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET, formatters: NO_FORMATTERS },
  ps1: { grammar: () => StreamLanguage.define(powerShell), label: "PowerShell", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  psm1: { grammar: () => StreamLanguage.define(powerShell), label: "PowerShell", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  psd1: { grammar: () => StreamLanguage.define(powerShell), label: "PowerShell", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  hs: { grammar: () => StreamLanguage.define(haskell), label: "Haskell", pretty: BRACKET, formatters: HASKELL_FORMATTERS },
  erl: { grammar: () => StreamLanguage.define(erlang), label: "Erlang", pretty: BRACKET, formatters: NO_FORMATTERS },
  hrl: { grammar: () => StreamLanguage.define(erlang), label: "Erlang", pretty: BRACKET, formatters: NO_FORMATTERS },
  elm: { grammar: () => StreamLanguage.define(elm), label: "Elm", pretty: BRACKET, formatters: NO_FORMATTERS },
  groovy: { grammar: () => StreamLanguage.define(groovy), label: "Groovy", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  gradle: { grammar: () => StreamLanguage.define(groovy), label: "Gradle", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  diff: { grammar: () => StreamLanguage.define(diff), label: "Diff", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  patch: { grammar: () => StreamLanguage.define(diff), label: "Diff", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  properties: { grammar: () => StreamLanguage.define(properties), label: "Properties", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  ini: { grammar: () => StreamLanguage.define(properties), label: "INI", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  env: { grammar: () => StreamLanguage.define(properties), label: "Dotenv", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cfg: { grammar: () => StreamLanguage.define(properties), label: "Config", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cmake: { grammar: () => StreamLanguage.define(cmake), label: "CMake", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  tcl: { grammar: () => StreamLanguage.define(tcl), label: "Tcl", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  cr: { grammar: () => StreamLanguage.define(crystal), label: "Crystal", pretty: BRACKET, formatters: NO_FORMATTERS },
  d: { grammar: () => StreamLanguage.define(d), label: "D", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  f: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  f90: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  f95: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  f03: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  pas: { grammar: () => StreamLanguage.define(pascal), label: "Pascal", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
  dart: { grammar: () => StreamLanguage.define(dart), label: "Dart", pretty: BRACKET_STATEMENTS, formatters: DART_FORMATTERS },
  sass: { grammar: () => StreamLanguage.define(sass), label: "Sass", pretty: BRACKET, formatters: NO_FORMATTERS },
  php: { grammar: () => wordLanguage(WORD_LANG_SPECS.php), label: "PHP", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  hack: { grammar: () => wordLanguage(WORD_LANG_SPECS.hack), label: "Hack", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  ex: { grammar: () => wordLanguage(WORD_LANG_SPECS.elixir), label: "Elixir", pretty: BRACKET, formatters: NO_FORMATTERS },
  exs: { grammar: () => wordLanguage(WORD_LANG_SPECS.elixir), label: "Elixir", pretty: BRACKET, formatters: NO_FORMATTERS },
  hcl: { grammar: () => wordLanguage(WORD_LANG_SPECS.hcl), label: "HCL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  tf: { grammar: () => wordLanguage(WORD_LANG_SPECS.hcl), label: "Terraform", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  tfvars: { grammar: () => wordLanguage(WORD_LANG_SPECS.hcl), label: "Terraform", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  sol: { grammar: () => wordLanguage(WORD_LANG_SPECS.solidity), label: "Solidity", pretty: BRACKET_STATEMENTS, formatters: SOLIDITY_FORMATTERS },
  move: { grammar: () => wordLanguage(WORD_LANG_SPECS.move), label: "Move", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  cairo: { grammar: () => wordLanguage(WORD_LANG_SPECS.cairo), label: "Cairo", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  circom: { grammar: () => wordLanguage(WORD_LANG_SPECS.circom), label: "Circom", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  cls: { grammar: () => wordLanguage(WORD_LANG_SPECS.apex), label: "Apex", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  trigger: { grammar: () => wordLanguage(WORD_LANG_SPECS.apex), label: "Apex", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  ql: { grammar: () => wordLanguage(WORD_LANG_SPECS.ql), label: "CodeQL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  qll: { grammar: () => wordLanguage(WORD_LANG_SPECS.ql), label: "CodeQL", pretty: BRACKET_STATEMENTS, formatters: NO_FORMATTERS },
  jsonnet: { grammar: () => wordLanguage(WORD_LANG_SPECS.jsonnet), label: "Jsonnet", pretty: DATA_WITH_COMMENTS, formatters: NO_FORMATTERS },
  libsonnet: { grammar: () => wordLanguage(WORD_LANG_SPECS.jsonnet), label: "Jsonnet", pretty: DATA_WITH_COMMENTS, formatters: NO_FORMATTERS },
  promql: { grammar: () => wordLanguage(WORD_LANG_SPECS.promql), label: "PromQL", pretty: BRACKET, formatters: NO_FORMATTERS },
  txt: { grammar: null, label: "Plain text", pretty: NO_LAYOUT, formatters: NO_FORMATTERS },
} satisfies Record<string, CatalogEntry>;

type CatalogKey = keyof typeof CATALOG;

function catalogEntry(ext: string): CatalogEntry | undefined {
  return Object.hasOwn(CATALOG, ext) ? CATALOG[ext as CatalogKey] : undefined;
}

/** Well-known extensionless, dot-prefixed, or extension-ambiguous filenames. */
const BASENAME_TO_EXT: Record<string, string> = {
  dockerfile: "dockerfile",
  containerfile: "dockerfile",
  "cmakelists.txt": "cmake",
  ".env": "env",
  ".gitignore": "properties",
  ".gitattributes": "properties",
  ".gitmodules": "properties",
  ".gitconfig": "properties",
  ".editorconfig": "properties",
  ".npmrc": "properties",
  gemfile: "rb",
  rakefile: "rb",
  vagrantfile: "rb",
  ".latexmkrc": "pl",
  ack: "pl",
  cpanfile: "pl",
  latexmkrc: "pl",
  rexfile: "pl",
};

const BASENAME_TO_LABEL: Record<string, string> = {
  dockerfile: "Dockerfile",
  containerfile: "Dockerfile",
  "cmakelists.txt": "CMake",
  ".env": "Dotenv",
  ".gitignore": "Git ignore",
  ".gitattributes": "Git attributes",
  ".gitmodules": "Git modules",
  ".gitconfig": "Git config",
  ".latexmkrc": "Perl",
  ack: "Perl",
  cpanfile: "Perl",
  latexmkrc: "Perl",
  rexfile: "Perl",
  ".editorconfig": "EditorConfig",
  ".npmrc": "npm config",
  gemfile: "Ruby",
  rakefile: "Ruby",
  vagrantfile: "Ruby",
  makefile: "Makefile",
  "go.mod": "Go module",
  "go.sum": "Go checksums",
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

export function languageLabelForPath(path: string): string {
  const base = basenameOf(path);
  const ext = extensionOf(base);
  const byName = BASENAME_TO_LABEL[base];
  if (byName) return byName;
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
    return "Plain text";
  }
  const known = DETECTED_LANGUAGE_TO_LABEL[key] ?? catalogEntry(key)?.label;
  if (known) return known;
  return formatSentenceCase(key);
}

/** Maps fenced-code labels to the viewer's language grammars. */
const INFO_TO_EXT: ReadonlyMap<string, string> = (() => {
  const map = new Map<string, string>();
  for (const ext of Object.keys(CATALOG)) map.set(ext, ext);
  for (const [ext, entry] of Object.entries(CATALOG) as [string, CatalogEntry][]) {
    const key = entry.label.toLowerCase();
    if (!map.has(key) && entry.grammar) map.set(key, ext);
  }
  return map;
})();

/** The `Language` a fence's info string names, or null to leave it plain. */
export function languageForInfoString(info: string): Language | null {
  // Fences carry attributes after the language (```ts title="x"); only the
  // first token names the grammar.
  const token = info.trim().split(/\s+/, 1)[0] ?? "";
  const name = token.replace(/^\./, "").toLowerCase();
  if (!name) return null;
  const ext = INFO_TO_EXT.get(name);
  const factory = ext ? catalogEntry(ext)?.grammar : undefined;
  if (!factory) return null;
  const value = factory();
  return value instanceof LanguageSupport ? value.language : value;
}

export function languageExtensionForPath(path: string): Extension | null {
  const factory = catalogEntryForPath(path)?.grammar;
  if (!factory) return null;
  const value = factory();
  // Stream languages use indentation because they provide no syntax fold ranges.
  return value instanceof StreamLanguage ? [value, indentFoldService] : value;
}

function catalogEntryForPath(path: string): CatalogEntry | undefined {
  const base = basenameOf(path);
  const ext = extensionOf(base);
  // Well-known names win over their extension, as `CMakeLists.txt` does over `.txt`.
  const named = BASENAME_TO_EXT[base];
  return named ? catalogEntry(named) : ext ? catalogEntry(ext) : undefined;
}

/** How Pretty lays out a file; a file outside the catalog has no layout. */
export function prettyLayoutForPath(path: string): PrettyLayout {
  return catalogEntryForPath(path)?.pretty ?? NO_LAYOUT;
}

/** Formatters whose configuration can set the file's line width, in priority order. */
export function formattersForPath(path: string): readonly FormatTool[] {
  return catalogEntryForPath(path)?.formatters ?? NO_FORMATTERS;
}

/** How Pretty lays out text named by a language, label or fence info string. */
export function prettyLayoutForLanguage(name: string): PrettyLayout {
  const token = (name.trim().split(/\s+/, 1)[0] ?? "").replace(/^\./, "").toLowerCase();
  const ext = INFO_TO_EXT.get(token);
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
