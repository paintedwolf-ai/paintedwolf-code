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

const EXT_TO_LANG: Record<string, () => LanguageSupport | Language> = {
  ts: () => javascript({ typescript: true }),
  tsx: () => javascript({ typescript: true, jsx: true }),
  js: () => javascript(),
  jsx: () => javascript({ jsx: true }),
  mjs: () => javascript(),
  cjs: () => javascript(),
  go: () => go(),
  rs: () => rust(),
  py: () => python(),
  pyi: () => python(),
  json: () => json(),
  jsonc: () => json(),
  yaml: () => yaml(),
  yml: () => yaml(),
  md: () => markdown({ codeLanguages: languageForInfoString }),
  mdx: () => markdown({ codeLanguages: languageForInfoString }),
  sh: () => StreamLanguage.define(shell),
  bash: () => StreamLanguage.define(shell),
  zsh: () => StreamLanguage.define(shell),
  c: () => StreamLanguage.define(c),
  h: () => StreamLanguage.define(c),
  cc: () => StreamLanguage.define(cpp),
  cpp: () => StreamLanguage.define(cpp),
  cxx: () => StreamLanguage.define(cpp),
  hpp: () => StreamLanguage.define(cpp),
  hh: () => StreamLanguage.define(cpp),
  hxx: () => StreamLanguage.define(cpp),
  java: () => StreamLanguage.define(java),
  cs: () => StreamLanguage.define(csharp),
  scala: () => StreamLanguage.define(scala),
  kt: () => StreamLanguage.define(kotlin),
  kts: () => StreamLanguage.define(kotlin),
  rb: () => StreamLanguage.define(ruby),
  swift: () => StreamLanguage.define(swift),
  lua: () => StreamLanguage.define(lua),
  r: () => StreamLanguage.define(r),
  jl: () => StreamLanguage.define(julia),
  clj: () => StreamLanguage.define(clojure),
  cljs: () => StreamLanguage.define(clojure),
  cljc: () => StreamLanguage.define(clojure),
  edn: () => StreamLanguage.define(clojure),
  scm: () => StreamLanguage.define(scheme),
  ss: () => StreamLanguage.define(scheme),
  lisp: () => StreamLanguage.define(commonLisp),
  cl: () => StreamLanguage.define(commonLisp),
  lsp: () => StreamLanguage.define(commonLisp),
  ml: () => StreamLanguage.define(oCaml),
  mli: () => StreamLanguage.define(oCaml),
  proto: () => StreamLanguage.define(protobuf),
  dockerfile: () => StreamLanguage.define(dockerFile),
  xml: () => StreamLanguage.define(xml),
  svg: () => StreamLanguage.define(xml),
  // The HTML parser applies embedded JavaScript and CSS grammars to component files.
  html: () => html(),
  htm: () => html(),
  vue: () => html(),
  svelte: () => html(),
  astro: () => html(),
  sql: () => StreamLanguage.define(standardSQL),
  css: () => cssLang(),
  // SCSS and Less nest rules and carry their own directives; `lang-css` parses
  // plain CSS only, so the preprocessor dialects stay on the stream modes.
  scss: () => StreamLanguage.define(sCSS),
  less: () => StreamLanguage.define(less),
  toml: () => StreamLanguage.define(toml),
  al: () => StreamLanguage.define(perl),
  perl: () => StreamLanguage.define(perl),
  ph: () => StreamLanguage.define(perl),
  pl: () => StreamLanguage.define(perl),
  plx: () => StreamLanguage.define(perl),
  pm: () => StreamLanguage.define(perl),
  psgi: () => StreamLanguage.define(perl),
  t: () => StreamLanguage.define(perl),
  ps1: () => StreamLanguage.define(powerShell),
  psm1: () => StreamLanguage.define(powerShell),
  psd1: () => StreamLanguage.define(powerShell),
  hs: () => StreamLanguage.define(haskell),
  erl: () => StreamLanguage.define(erlang),
  hrl: () => StreamLanguage.define(erlang),
  elm: () => StreamLanguage.define(elm),
  groovy: () => StreamLanguage.define(groovy),
  gradle: () => StreamLanguage.define(groovy),
  diff: () => StreamLanguage.define(diff),
  patch: () => StreamLanguage.define(diff),
  properties: () => StreamLanguage.define(properties),
  ini: () => StreamLanguage.define(properties),
  env: () => StreamLanguage.define(properties),
  cfg: () => StreamLanguage.define(properties),
  cmake: () => StreamLanguage.define(cmake),
  tcl: () => StreamLanguage.define(tcl),
  cr: () => StreamLanguage.define(crystal),
  d: () => StreamLanguage.define(d),
  f: () => StreamLanguage.define(fortran),
  f90: () => StreamLanguage.define(fortran),
  f95: () => StreamLanguage.define(fortran),
  f03: () => StreamLanguage.define(fortran),
  pas: () => StreamLanguage.define(pascal),
  dart: () => StreamLanguage.define(dart),
  sass: () => StreamLanguage.define(sass),
  php: () => wordLanguage(WORD_LANG_SPECS.php),
  hack: () => wordLanguage(WORD_LANG_SPECS.hack),
  ex: () => wordLanguage(WORD_LANG_SPECS.elixir),
  exs: () => wordLanguage(WORD_LANG_SPECS.elixir),
  hcl: () => wordLanguage(WORD_LANG_SPECS.hcl),
  tf: () => wordLanguage(WORD_LANG_SPECS.hcl),
  tfvars: () => wordLanguage(WORD_LANG_SPECS.hcl),
  sol: () => wordLanguage(WORD_LANG_SPECS.solidity),
  move: () => wordLanguage(WORD_LANG_SPECS.move),
  cairo: () => wordLanguage(WORD_LANG_SPECS.cairo),
  circom: () => wordLanguage(WORD_LANG_SPECS.circom),
  cls: () => wordLanguage(WORD_LANG_SPECS.apex),
  trigger: () => wordLanguage(WORD_LANG_SPECS.apex),
  ql: () => wordLanguage(WORD_LANG_SPECS.ql),
  qll: () => wordLanguage(WORD_LANG_SPECS.ql),
  jsonnet: () => wordLanguage(WORD_LANG_SPECS.jsonnet),
  libsonnet: () => wordLanguage(WORD_LANG_SPECS.jsonnet),
  promql: () => wordLanguage(WORD_LANG_SPECS.promql),
};

const EXT_TO_LABEL: Record<string, string> = {
  ts: "TypeScript",
  tsx: "TSX",
  js: "JavaScript",
  jsx: "JSX",
  mjs: "JavaScript",
  cjs: "JavaScript",
  go: "Go",
  rs: "Rust",
  py: "Python",
  pyi: "Python",
  json: "JSON",
  jsonc: "JSON",
  yaml: "YAML",
  yml: "YAML",
  txt: "Plain text",
  md: "Markdown",
  mdx: "Markdown",
  sh: "Shell",
  bash: "Shell",
  zsh: "Shell",
  c: "C",
  h: "C",
  cc: "C++",
  cpp: "C++",
  cxx: "C++",
  hpp: "C++",
  hh: "C++",
  hxx: "C++",
  java: "Java",
  cs: "C#",
  scala: "Scala",
  kt: "Kotlin",
  kts: "Kotlin",
  rb: "Ruby",
  swift: "Swift",
  lua: "Lua",
  r: "R",
  jl: "Julia",
  clj: "Clojure",
  cljs: "Clojure",
  cljc: "Clojure",
  edn: "Clojure",
  scm: "Scheme",
  ss: "Scheme",
  lisp: "Common Lisp",
  cl: "Common Lisp",
  lsp: "Common Lisp",
  ml: "OCaml",
  mli: "OCaml",
  proto: "Protobuf",
  dockerfile: "Dockerfile",
  xml: "XML",
  svg: "SVG",
  html: "HTML",
  htm: "HTML",
  vue: "Vue",
  svelte: "Svelte",
  astro: "Astro",
  sql: "SQL",
  css: "CSS",
  scss: "SCSS",
  less: "Less",
  toml: "TOML",
  al: "Perl",
  perl: "Perl",
  ph: "Perl",
  pl: "Perl",
  plx: "Perl",
  pm: "Perl",
  psgi: "Perl",
  t: "Perl",
  ps1: "PowerShell",
  psm1: "PowerShell",
  psd1: "PowerShell",
  hs: "Haskell",
  erl: "Erlang",
  hrl: "Erlang",
  elm: "Elm",
  groovy: "Groovy",
  gradle: "Gradle",
  diff: "Diff",
  patch: "Diff",
  properties: "Properties",
  ini: "INI",
  env: "Dotenv",
  cfg: "Config",
  cmake: "CMake",
  tcl: "Tcl",
  cr: "Crystal",
  d: "D",
  f: "Fortran",
  f90: "Fortran",
  f95: "Fortran",
  f03: "Fortran",
  pas: "Pascal",
  php: "PHP",
  dart: "Dart",
  ex: "Elixir",
  exs: "Elixir",
  sass: "Sass",
  sol: "Solidity",
  hcl: "HCL",
  tf: "Terraform",
  tfvars: "Terraform",
  cls: "Apex",
  trigger: "Apex",
  cairo: "Cairo",
  circom: "Circom",
  move: "Move",
  promql: "PromQL",
  ql: "CodeQL",
  qll: "CodeQL",
  jsonnet: "Jsonnet",
  libsonnet: "Jsonnet",
  hack: "Hack",
};

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
  if (ext && EXT_TO_LABEL[ext]) return EXT_TO_LABEL[ext];
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
  const known = DETECTED_LANGUAGE_TO_LABEL[key] ?? EXT_TO_LABEL[key];
  if (known) return known;
  return formatSentenceCase(key);
}

/** Maps fenced-code labels to the viewer's language grammars. */
const INFO_TO_EXT: ReadonlyMap<string, string> = (() => {
  const map = new Map<string, string>();
  for (const ext of Object.keys(EXT_TO_LANG)) map.set(ext, ext);
  for (const [ext, label] of Object.entries(EXT_TO_LABEL)) {
    const key = label.toLowerCase();
    if (!map.has(key) && EXT_TO_LANG[ext]) map.set(key, ext);
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
  const factory = ext ? EXT_TO_LANG[ext] : undefined;
  if (!factory) return null;
  const value = factory();
  return value instanceof LanguageSupport ? value.language : value;
}

export function languageExtensionForPath(path: string): Extension | null {
  const base = basenameOf(path);
  const ext = extensionOf(base);
  const factory =
    (ext ? EXT_TO_LANG[ext] : undefined) ??
    (BASENAME_TO_EXT[base] ? EXT_TO_LANG[BASENAME_TO_EXT[base]] : undefined);
  if (!factory) return null;
  const value = factory();
  // Stream languages use indentation because they provide no syntax fold ranges.
  return value instanceof StreamLanguage ? [value, indentFoldService] : value;
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
