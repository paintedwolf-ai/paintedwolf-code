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

/** Each catalog entry: its grammar (null for plain text), its label, and how Pretty lays it out. */
interface CatalogEntry {
  readonly grammar: (() => LanguageSupport | Language) | null;
  readonly label: string;
  readonly pretty: PrettyLayout;
}

const CATALOG = {
  ts: { grammar: () => javascript({ typescript: true }), label: "TypeScript", pretty: SCRIPT },
  tsx: { grammar: () => javascript({ typescript: true, jsx: true }), label: "TSX", pretty: SCRIPT },
  js: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT },
  jsx: { grammar: () => javascript({ jsx: true }), label: "JSX", pretty: SCRIPT },
  mjs: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT },
  cjs: { grammar: () => javascript(), label: "JavaScript", pretty: SCRIPT },
  go: { grammar: () => go(), label: "Go", pretty: BRACKET },
  rs: { grammar: () => rust(), label: "Rust", pretty: BRACKET_STATEMENTS },
  py: { grammar: () => python(), label: "Python", pretty: BRACKET },
  pyi: { grammar: () => python(), label: "Python", pretty: BRACKET },
  json: { grammar: () => json(), label: "JSON", pretty: DATA },
  jsonc: { grammar: () => json(), label: "JSON", pretty: DATA_WITH_COMMENTS },
  json5: { grammar: () => json(), label: "JSON5", pretty: DATA_WITH_COMMENTS },
  jsonl: { grammar: () => json(), label: "JSON Lines", pretty: DATA_RECORDS },
  ndjson: { grammar: () => json(), label: "NDJSON", pretty: DATA_RECORDS },
  webmanifest: { grammar: () => json(), label: "Web app manifest", pretty: DATA },
  har: { grammar: () => json(), label: "HAR", pretty: DATA },
  avsc: { grammar: () => json(), label: "Avro schema", pretty: DATA },
  yaml: { grammar: () => yaml(), label: "YAML", pretty: BRACKET },
  yml: { grammar: () => yaml(), label: "YAML", pretty: BRACKET },
  md: { grammar: () => markdown({ codeLanguages: languageForInfoString }), label: "Markdown", pretty: FENCED },
  mdx: { grammar: () => markdown({ codeLanguages: languageForInfoString }), label: "Markdown", pretty: FENCED },
  sh: { grammar: () => StreamLanguage.define(shell), label: "Shell", pretty: NO_LAYOUT },
  bash: { grammar: () => StreamLanguage.define(shell), label: "Shell", pretty: NO_LAYOUT },
  zsh: { grammar: () => StreamLanguage.define(shell), label: "Shell", pretty: NO_LAYOUT },
  c: { grammar: () => StreamLanguage.define(c), label: "C", pretty: BRACKET_STATEMENTS },
  h: { grammar: () => StreamLanguage.define(c), label: "C", pretty: BRACKET_STATEMENTS },
  cc: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS },
  cpp: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS },
  cxx: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS },
  hpp: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS },
  hh: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS },
  hxx: { grammar: () => StreamLanguage.define(cpp), label: "C++", pretty: BRACKET_STATEMENTS },
  java: { grammar: () => StreamLanguage.define(java), label: "Java", pretty: BRACKET_STATEMENTS },
  cs: { grammar: () => StreamLanguage.define(csharp), label: "C#", pretty: BRACKET_STATEMENTS },
  scala: { grammar: () => StreamLanguage.define(scala), label: "Scala", pretty: BRACKET_STATEMENTS },
  kt: { grammar: () => StreamLanguage.define(kotlin), label: "Kotlin", pretty: BRACKET_STATEMENTS },
  kts: { grammar: () => StreamLanguage.define(kotlin), label: "Kotlin", pretty: BRACKET_STATEMENTS },
  rb: { grammar: () => StreamLanguage.define(ruby), label: "Ruby", pretty: BRACKET },
  swift: { grammar: () => StreamLanguage.define(swift), label: "Swift", pretty: BRACKET_STATEMENTS },
  lua: { grammar: () => StreamLanguage.define(lua), label: "Lua", pretty: BRACKET },
  r: { grammar: () => StreamLanguage.define(r), label: "R", pretty: BRACKET },
  jl: { grammar: () => StreamLanguage.define(julia), label: "Julia", pretty: BRACKET },
  clj: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT },
  cljs: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT },
  cljc: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT },
  edn: { grammar: () => StreamLanguage.define(clojure), label: "Clojure", pretty: NO_LAYOUT },
  scm: { grammar: () => StreamLanguage.define(scheme), label: "Scheme", pretty: NO_LAYOUT },
  ss: { grammar: () => StreamLanguage.define(scheme), label: "Scheme", pretty: NO_LAYOUT },
  lisp: { grammar: () => StreamLanguage.define(commonLisp), label: "Common Lisp", pretty: NO_LAYOUT },
  cl: { grammar: () => StreamLanguage.define(commonLisp), label: "Common Lisp", pretty: NO_LAYOUT },
  lsp: { grammar: () => StreamLanguage.define(commonLisp), label: "Common Lisp", pretty: NO_LAYOUT },
  ml: { grammar: () => StreamLanguage.define(oCaml), label: "OCaml", pretty: BRACKET },
  mli: { grammar: () => StreamLanguage.define(oCaml), label: "OCaml", pretty: BRACKET },
  proto: { grammar: () => StreamLanguage.define(protobuf), label: "Protobuf", pretty: BRACKET_STATEMENTS },
  dockerfile: { grammar: () => StreamLanguage.define(dockerFile), label: "Dockerfile", pretty: NO_LAYOUT },
  xml: { grammar: () => StreamLanguage.define(xml), label: "XML", pretty: MARKUP },
  svg: { grammar: () => StreamLanguage.define(xml), label: "SVG", pretty: MARKUP },
  // The HTML parser applies embedded JavaScript and CSS grammars to component files.
  html: { grammar: () => html(), label: "HTML", pretty: MARKUP },
  htm: { grammar: () => html(), label: "HTML", pretty: MARKUP },
  vue: { grammar: () => html(), label: "Vue", pretty: MARKUP },
  svelte: { grammar: () => html(), label: "Svelte", pretty: MARKUP },
  astro: { grammar: () => html(), label: "Astro", pretty: MARKUP },
  sql: { grammar: () => StreamLanguage.define(standardSQL), label: "SQL", pretty: CLAUSE },
  css: { grammar: () => cssLang(), label: "CSS", pretty: STYLE },
  // SCSS and Less nest rules and carry their own directives; `lang-css` parses
  // plain CSS only, so the preprocessor dialects stay on the stream modes.
  scss: { grammar: () => StreamLanguage.define(sCSS), label: "SCSS", pretty: STYLE_WITH_LINE_COMMENTS },
  less: { grammar: () => StreamLanguage.define(less), label: "Less", pretty: STYLE_WITH_LINE_COMMENTS },
  toml: { grammar: () => StreamLanguage.define(toml), label: "TOML", pretty: BRACKET },
  al: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  perl: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  ph: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  pl: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  plx: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  pm: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  psgi: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  t: { grammar: () => StreamLanguage.define(perl), label: "Perl", pretty: BRACKET },
  ps1: { grammar: () => StreamLanguage.define(powerShell), label: "PowerShell", pretty: NO_LAYOUT },
  psm1: { grammar: () => StreamLanguage.define(powerShell), label: "PowerShell", pretty: NO_LAYOUT },
  psd1: { grammar: () => StreamLanguage.define(powerShell), label: "PowerShell", pretty: NO_LAYOUT },
  hs: { grammar: () => StreamLanguage.define(haskell), label: "Haskell", pretty: BRACKET },
  erl: { grammar: () => StreamLanguage.define(erlang), label: "Erlang", pretty: BRACKET },
  hrl: { grammar: () => StreamLanguage.define(erlang), label: "Erlang", pretty: BRACKET },
  elm: { grammar: () => StreamLanguage.define(elm), label: "Elm", pretty: BRACKET },
  groovy: { grammar: () => StreamLanguage.define(groovy), label: "Groovy", pretty: BRACKET_STATEMENTS },
  gradle: { grammar: () => StreamLanguage.define(groovy), label: "Gradle", pretty: BRACKET_STATEMENTS },
  diff: { grammar: () => StreamLanguage.define(diff), label: "Diff", pretty: NO_LAYOUT },
  patch: { grammar: () => StreamLanguage.define(diff), label: "Diff", pretty: NO_LAYOUT },
  properties: { grammar: () => StreamLanguage.define(properties), label: "Properties", pretty: NO_LAYOUT },
  ini: { grammar: () => StreamLanguage.define(properties), label: "INI", pretty: NO_LAYOUT },
  env: { grammar: () => StreamLanguage.define(properties), label: "Dotenv", pretty: NO_LAYOUT },
  cfg: { grammar: () => StreamLanguage.define(properties), label: "Config", pretty: NO_LAYOUT },
  cmake: { grammar: () => StreamLanguage.define(cmake), label: "CMake", pretty: NO_LAYOUT },
  tcl: { grammar: () => StreamLanguage.define(tcl), label: "Tcl", pretty: NO_LAYOUT },
  cr: { grammar: () => StreamLanguage.define(crystal), label: "Crystal", pretty: BRACKET },
  d: { grammar: () => StreamLanguage.define(d), label: "D", pretty: BRACKET_STATEMENTS },
  f: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT },
  f90: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT },
  f95: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT },
  f03: { grammar: () => StreamLanguage.define(fortran), label: "Fortran", pretty: NO_LAYOUT },
  pas: { grammar: () => StreamLanguage.define(pascal), label: "Pascal", pretty: NO_LAYOUT },
  dart: { grammar: () => StreamLanguage.define(dart), label: "Dart", pretty: BRACKET_STATEMENTS },
  sass: { grammar: () => StreamLanguage.define(sass), label: "Sass", pretty: BRACKET },
  php: { grammar: () => wordLanguage(WORD_LANG_SPECS.php), label: "PHP", pretty: BRACKET_STATEMENTS },
  hack: { grammar: () => wordLanguage(WORD_LANG_SPECS.hack), label: "Hack", pretty: BRACKET_STATEMENTS },
  ex: { grammar: () => wordLanguage(WORD_LANG_SPECS.elixir), label: "Elixir", pretty: BRACKET },
  exs: { grammar: () => wordLanguage(WORD_LANG_SPECS.elixir), label: "Elixir", pretty: BRACKET },
  hcl: { grammar: () => wordLanguage(WORD_LANG_SPECS.hcl), label: "HCL", pretty: BRACKET_STATEMENTS },
  tf: { grammar: () => wordLanguage(WORD_LANG_SPECS.hcl), label: "Terraform", pretty: BRACKET_STATEMENTS },
  tfvars: { grammar: () => wordLanguage(WORD_LANG_SPECS.hcl), label: "Terraform", pretty: BRACKET_STATEMENTS },
  sol: { grammar: () => wordLanguage(WORD_LANG_SPECS.solidity), label: "Solidity", pretty: BRACKET_STATEMENTS },
  move: { grammar: () => wordLanguage(WORD_LANG_SPECS.move), label: "Move", pretty: BRACKET_STATEMENTS },
  cairo: { grammar: () => wordLanguage(WORD_LANG_SPECS.cairo), label: "Cairo", pretty: BRACKET_STATEMENTS },
  circom: { grammar: () => wordLanguage(WORD_LANG_SPECS.circom), label: "Circom", pretty: BRACKET_STATEMENTS },
  cls: { grammar: () => wordLanguage(WORD_LANG_SPECS.apex), label: "Apex", pretty: BRACKET_STATEMENTS },
  trigger: { grammar: () => wordLanguage(WORD_LANG_SPECS.apex), label: "Apex", pretty: BRACKET_STATEMENTS },
  ql: { grammar: () => wordLanguage(WORD_LANG_SPECS.ql), label: "CodeQL", pretty: BRACKET_STATEMENTS },
  qll: { grammar: () => wordLanguage(WORD_LANG_SPECS.ql), label: "CodeQL", pretty: BRACKET_STATEMENTS },
  jsonnet: { grammar: () => wordLanguage(WORD_LANG_SPECS.jsonnet), label: "Jsonnet", pretty: DATA_WITH_COMMENTS },
  libsonnet: { grammar: () => wordLanguage(WORD_LANG_SPECS.jsonnet), label: "Jsonnet", pretty: DATA_WITH_COMMENTS },
  promql: { grammar: () => wordLanguage(WORD_LANG_SPECS.promql), label: "PromQL", pretty: BRACKET },
  txt: { grammar: null, label: "Plain text", pretty: NO_LAYOUT },
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
