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
import { LanguageSupport, StreamLanguage, type Language, type StreamParser } from "@codemirror/language";
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
  BRACKET, BRACKET_BLOCKS, BRACKET_STATEMENTS, CHAIN, CLAUSE, CONTAINERS,
  DATA, DATA_RECORDS, DATA_WITH_COMMENTS, FENCED, MARKUP, NO_LAYOUT,
  type PrettyLayout, SCRIPT, SCRIPT_JSX, SEXP, SEXP_DATA, STYLE, STYLE_WITH_LINE_COMMENTS,
} from "../pretty/pretty-outcome.ts";

const words = (s: string): string[] => s.trim().split(/\s+/);

// Languages without bundled grammars use reference keyword lists and a shared tokenizer.
const WORD_LANG_SPECS = {
  php: {
    name: "php",
    keywords: words(
      "abstract and array as break callable case catch class clone const continue declare default do echo else elseif " +
      "empty enddeclare endfor endforeach endif endswitch endwhile enum extends final finally fn for foreach function " +
      "global goto if implements include include_once instanceof insteadof interface isset list match namespace new or " +
      "print private protected public readonly require require_once return static switch throw trait try unset use var while xor yield",
    ),
    types: words("int float bool string void mixed object iterable never self parent"),
    atoms: words("true false null this"),
    dollarVars: true,
    atMeta: true,
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
    metaPatterns: [/^<\?(?:php|=)?/, /^\?>/],
    caseInsensitive: true,
  },
  hack: {
    name: "hack",
    keywords: words(
      "abstract arraykey as async await break case catch class clone concurrent const continue default do echo else elseif " +
      "enum extends final finally for foreach function if implements include instanceof interface is namespace new newtype " +
      "print private protected public require return shape static switch throw trait try tuple type use while yield",
    ),
    types: words("int float bool string void mixed dynamic num nonnull vec dict keyset Vector Map Set Awaitable"),
    atoms: words("true false null this"),
    dollarVars: true,
    atMeta: true,
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
  },
  elixir: {
    name: "elixir",
    keywords: words(
      "def defp defmodule defmacro defmacrop defstruct defprotocol defimpl defdelegate defguard defguardp defexception " +
      "do end fn case cond if unless else after rescue catch try receive for when in and or not raise throw import " +
      "require alias use quote unquote with super",
    ),
    atoms: words("true false nil self"),
    colonAtoms: true,
    atMeta: true,
    capitalizedTypes: true,
    lineComments: ["#"],
  },
  hcl: {
    name: "hcl",
    keywords: words(
      "resource data variable output module provider terraform locals dynamic content lifecycle backend provisioner " +
      "connection moved import check removed for in if else endif endfor",
    ),
    atoms: words("true false null var local each count self path"),
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
  },
  solidity: {
    name: "solidity",
    keywords: words(
      "pragma solidity contract interface library function modifier constructor event error emit revert require assert " +
      "mapping struct enum public private internal external pure view payable storage memory calldata returns return if " +
      "else for while do break continue new delete import is using try catch abstract override virtual immutable constant " +
      "unchecked assembly type indexed anonymous fallback receive",
    ),
    types: words("address bool string bytes byte int uint"),
    typePattern: /^(?:u?int|bytes)\d+$/,
    atoms: words("true false wei gwei ether seconds minutes hours days weeks msg block tx this super now"),
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  move: {
    name: "move",
    keywords: words(
      "module script fun public entry native struct has copy drop store key use as friend const let mut return abort if " +
      "else while loop break continue spec schema invariant assume assert move acquires phantom",
    ),
    types: words("u8 u16 u32 u64 u128 u256 bool address vector signer"),
    atoms: words("true false"),
    atMeta: true,
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  cairo: {
    name: "cairo",
    keywords: words(
      "use mod fn struct enum trait impl of let mut ref return if else match loop while for break continue const static " +
      "type as pub extern nopanic implicits",
    ),
    types: words("felt252 usize bool u8 u16 u32 u64 u128 u256 i8 i16 i32 i64 i128 ContractAddress"),
    atoms: words("true false Some None Ok Err"),
    lineComments: ["//"],
  },
  circom: {
    name: "circom",
    keywords: words(
      "pragma circom include template component signal input output public var function return if else for while do " +
      "assert log parallel custom main",
    ),
    atoms: words("true false"),
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  apex: {
    name: "apex",
    keywords: words(
      "public private protected global static final abstract virtual override class interface extends implements trigger " +
      "on new return if else for while do break continue try catch finally throw insert update upsert delete undelete " +
      "merge testmethod webservice with without sharing inherited get set enum switch when select from where limit order by",
    ),
    types: words("void String Integer Boolean Decimal Double Long Date Datetime Time Id List Set Map Object SObject Blob"),
    atoms: words("true false null this super"),
    atMeta: true,
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
    caseInsensitive: true,
  },
  ql: {
    name: "ql",
    keywords: words(
      "import module class extends implements predicate query from where select as order by asc desc exists forall forex " +
      "not and or implies if then else instanceof in newtype cached abstract final private deprecated pragma language " +
      "bindingset signature default external transient",
    ),
    types: words("int float string boolean date"),
    atoms: words("true false none this result super any"),
    atMeta: true,
    lineComments: ["//"],
    blockComment: ["/*", "*/"],
  },
  jsonnet: {
    name: "jsonnet",
    keywords: words("local function if then else for in import importstr importbin error assert tailstrict"),
    atoms: words("true false null self super"),
    lineComments: ["//", "#"],
    blockComment: ["/*", "*/"],
  },
  promql: {
    name: "promql",
    keywords: words(
      "by without on ignoring group_left group_right offset bool and or unless atan2 start end sum avg min max count " +
      "count_values stddev stdvar topk bottomk quantile rate irate increase delta idelta histogram_quantile label_replace " +
      "label_join vector time absent absent_over_time ceil floor round clamp clamp_min clamp_max exp ln log2 log10 sqrt " +
      "abs changes resets predict_linear deriv avg_over_time min_over_time max_over_time sum_over_time count_over_time " +
      "quantile_over_time last_over_time present_over_time sort sort_desc timestamp scalar",
    ),
    atoms: words("Inf NaN"),
    lineComments: ["#"],
  },
} satisfies Record<string, WordLangSpec>;

/** Formatters whose configuration sets a language's line width, in priority order. */
const WEB_FORMATTERS = ["prettier", "biome", "dprint"] as const satisfies readonly FormatTool[];
const MARKUP_FORMATTERS = ["prettier", "dprint"] as const satisfies readonly FormatTool[];
const PYTHON_FORMATTERS = ["ruff", "black"] as const satisfies readonly FormatTool[];
const GO_FORMATTERS = ["golangci"] as const satisfies readonly FormatTool[];
const RUST_FORMATTERS = ["rustfmt"] as const satisfies readonly FormatTool[];
const CLANG_FORMATTERS = ["clang_format"] as const satisfies readonly FormatTool[];
const JAVA_FORMATTERS = ["checkstyle", "clang_format"] as const satisfies readonly FormatTool[];
const SCALA_FORMATTERS = ["scalafmt"] as const satisfies readonly FormatTool[];
const SWIFT_FORMATTERS = ["swift_format"] as const satisfies readonly FormatTool[];
const DART_FORMATTERS = ["dart"] as const satisfies readonly FormatTool[];
const RUBY_FORMATTERS = ["rubocop"] as const satisfies readonly FormatTool[];
const PHP_FORMATTERS = ["phpcs", "prettier"] as const satisfies readonly FormatTool[];
const ELIXIR_FORMATTERS = ["mix_format"] as const satisfies readonly FormatTool[];
const LUA_FORMATTERS = ["stylua"] as const satisfies readonly FormatTool[];
const OCAML_FORMATTERS = ["ocamlformat"] as const satisfies readonly FormatTool[];
const HASKELL_FORMATTERS = ["fourmolu"] as const satisfies readonly FormatTool[];
const JULIA_FORMATTERS = ["julia_formatter"] as const satisfies readonly FormatTool[];
const SQL_FORMATTERS = ["sqlfluff"] as const satisfies readonly FormatTool[];
const SOLIDITY_FORMATTERS = ["foundry", "prettier"] as const satisfies readonly FormatTool[];
const TOML_FORMATTERS = ["taplo", "dprint"] as const satisfies readonly FormatTool[];
const NO_FORMATTERS = [] as const satisfies readonly FormatTool[];

export const PLAIN_TEXT_LABEL = "Plain text";

/** Each catalog entry: its grammar (null for plain text), its label, how Pretty lays it out, and its formatters. */
interface CatalogEntry {
  readonly grammar: (() => LanguageSupport | Language) | null;
  readonly label: string;
  readonly pretty: PrettyLayout;
  readonly formatters: readonly FormatTool[];
}

const langEntry = (
  grammar: () => LanguageSupport | Language,
  label: string,
  pretty: PrettyLayout,
  formatters: readonly FormatTool[] = NO_FORMATTERS,
): CatalogEntry => ({ grammar, label, pretty, formatters });

const streamEntry = <S>(
  parser: StreamParser<S>,
  label: string,
  pretty: PrettyLayout,
  formatters: readonly FormatTool[] = NO_FORMATTERS,
): CatalogEntry => ({ grammar: () => defineStream(parser), label, pretty, formatters });

const wordEntry = (
  spec: WordLangSpec,
  label: string,
  pretty: PrettyLayout,
  formatters: readonly FormatTool[] = NO_FORMATTERS,
): CatalogEntry => ({ grammar: () => defineWord(spec), label, pretty, formatters });

const TS_ENTRY = langEntry(() => javascript({ typescript: true }), "TypeScript", SCRIPT, WEB_FORMATTERS);
const JS_ENTRY = langEntry(() => javascript(), "JavaScript", SCRIPT, WEB_FORMATTERS);
const MD_ENTRY = langEntry(() => markdown({ codeLanguages: languageForInfoString }), "Markdown", FENCED, MARKUP_FORMATTERS);
const PY_ENTRY = langEntry(python, "Python", BRACKET, PYTHON_FORMATTERS);
const XML_ENTRY = streamEntry(xml, "XML", MARKUP, MARKUP_FORMATTERS);
const SH_ENTRY = streamEntry(shell, "Shell", CHAIN);
const C_ENTRY = streamEntry(c, "C", BRACKET_STATEMENTS, CLANG_FORMATTERS);
const CPP_ENTRY = streamEntry(cpp, "C++", BRACKET_STATEMENTS, CLANG_FORMATTERS);
const CSHARP_ENTRY = streamEntry(csharp, "C#", BRACKET_STATEMENTS, CLANG_FORMATTERS);
const KT_ENTRY = streamEntry(kotlin, "Kotlin", BRACKET_STATEMENTS);
const RB_ENTRY = streamEntry(ruby, "Ruby", BRACKET, RUBY_FORMATTERS);
const CLJ_ENTRY = streamEntry(clojure, "Clojure", SEXP);
const SCM_ENTRY = streamEntry(scheme, "Scheme", SEXP);
const LISP_ENTRY = streamEntry(commonLisp, "Common Lisp", SEXP);
const ML_ENTRY = streamEntry(oCaml, "OCaml", BRACKET, OCAML_FORMATTERS);
const PERL_ENTRY = streamEntry(perl, "Perl", BRACKET);
const PS_ENTRY = streamEntry(powerShell, "PowerShell", BRACKET_STATEMENTS);
const ERL_ENTRY = streamEntry(erlang, "Erlang", BRACKET);
const GROOVY_ENTRY = streamEntry(groovy, "Groovy", BRACKET_STATEMENTS, JAVA_FORMATTERS);
const DIFF_ENTRY = streamEntry(diff, "Diff", NO_LAYOUT);
const FORTRAN_ENTRY = streamEntry(fortran, "Fortran", NO_LAYOUT);
const EX_ENTRY = wordEntry(WORD_LANG_SPECS.elixir, "Elixir", BRACKET, ELIXIR_FORMATTERS);
const HCL_ENTRY = wordEntry(WORD_LANG_SPECS.hcl, "HCL", BRACKET_STATEMENTS);
const TF_ENTRY = wordEntry(WORD_LANG_SPECS.hcl, "Terraform", BRACKET_STATEMENTS);
const APEX_ENTRY = wordEntry(WORD_LANG_SPECS.apex, "Apex", BRACKET_STATEMENTS);
const QL_ENTRY = wordEntry(WORD_LANG_SPECS.ql, "CodeQL", BRACKET_STATEMENTS);
const JSONNET_ENTRY = wordEntry(WORD_LANG_SPECS.jsonnet, "Jsonnet", DATA_WITH_COMMENTS);
const LOG_ENTRY: CatalogEntry = { grammar: null, label: "Log", pretty: CONTAINERS, formatters: NO_FORMATTERS };

function entriesFor<const K extends string>(exts: readonly K[], entry: CatalogEntry): Record<K, CatalogEntry> {
  const out = {} as Record<K, CatalogEntry>;
  for (const ext of exts) out[ext] = entry;
  return out;
}

const CATALOG = {
  ...entriesFor(["ts", "mts", "cts"], TS_ENTRY),
  tsx: langEntry(() => javascript({ typescript: true, jsx: true }), "TSX", SCRIPT_JSX, WEB_FORMATTERS),
  ...entriesFor(["js", "mjs", "cjs"], JS_ENTRY),
  jsx: langEntry(() => javascript({ jsx: true }), "JSX", SCRIPT_JSX, WEB_FORMATTERS),
  go: langEntry(go, "Go", BRACKET_BLOCKS, GO_FORMATTERS),
  rs: langEntry(rust, "Rust", BRACKET_STATEMENTS, RUST_FORMATTERS),
  ...entriesFor(["py", "pyi"], PY_ENTRY),
  json: langEntry(json, "JSON", DATA, WEB_FORMATTERS),
  jsonc: langEntry(json, "JSON", DATA_WITH_COMMENTS, WEB_FORMATTERS),
  json5: langEntry(json, "JSON5", DATA_WITH_COMMENTS, WEB_FORMATTERS),
  jsonl: langEntry(json, "JSON Lines", DATA_RECORDS),
  ndjson: langEntry(json, "NDJSON", DATA_RECORDS),
  webmanifest: langEntry(json, "Web app manifest", DATA, WEB_FORMATTERS),
  har: langEntry(json, "HAR", DATA, WEB_FORMATTERS),
  avsc: langEntry(json, "Avro schema", DATA, WEB_FORMATTERS),
  geojson: langEntry(json, "GeoJSON", DATA, WEB_FORMATTERS),
  topojson: langEntry(json, "TopoJSON", DATA, WEB_FORMATTERS),
  sarif: langEntry(json, "SARIF", DATA, WEB_FORMATTERS),
  tfstate: langEntry(json, "Terraform state", DATA),
  ...entriesFor(["yaml", "yml"], langEntry(yaml, "YAML", BRACKET, MARKUP_FORMATTERS)),
  ...entriesFor(["md", "mdx"], MD_ENTRY),
  ...entriesFor(["sh", "bash", "zsh"], SH_ENTRY),
  ...entriesFor(["c", "h"], C_ENTRY),
  ...entriesFor(["cc", "cpp", "cxx", "hpp", "hh", "hxx"], CPP_ENTRY),
  java: streamEntry(java, "Java", BRACKET_STATEMENTS, JAVA_FORMATTERS),
  ...entriesFor(["cs", "csx", "cake"], CSHARP_ENTRY),
  scala: streamEntry(scala, "Scala", BRACKET_STATEMENTS, SCALA_FORMATTERS),
  ...entriesFor(["kt", "kts"], KT_ENTRY),
  ...entriesFor(["rb", "rake", "gemspec", "ru"], RB_ENTRY),
  swift: streamEntry(swift, "Swift", BRACKET_STATEMENTS, SWIFT_FORMATTERS),
  lua: streamEntry(lua, "Lua", BRACKET, LUA_FORMATTERS),
  r: streamEntry(r, "R", BRACKET),
  jl: streamEntry(julia, "Julia", BRACKET, JULIA_FORMATTERS),
  ...entriesFor(["clj", "cljs", "cljc"], CLJ_ENTRY),
  edn: streamEntry(clojure, "Clojure", SEXP_DATA),
  ...entriesFor(["scm", "ss"], SCM_ENTRY),
  ...entriesFor(["lisp", "cl", "lsp"], LISP_ENTRY),
  ...entriesFor(["ml", "mli"], ML_ENTRY),
  proto: streamEntry(protobuf, "Protobuf", BRACKET_STATEMENTS, CLANG_FORMATTERS),
  dockerfile: streamEntry(dockerFile, "Dockerfile", CHAIN),
  ...entriesFor(["xml", "csproj", "vbproj", "fsproj", "vcxproj", "props", "targets", "nuspec", "xaml", "plist", "xsd", "wsdl"], XML_ENTRY),
  svg: streamEntry(xml, "SVG", MARKUP, MARKUP_FORMATTERS),
  // The HTML parser applies embedded JavaScript and CSS grammars to component files.
  ...entriesFor(["html", "htm"], langEntry(html, "HTML", MARKUP, MARKUP_FORMATTERS)),
  vue: langEntry(html, "Vue", MARKUP, MARKUP_FORMATTERS),
  svelte: langEntry(html, "Svelte", MARKUP, MARKUP_FORMATTERS),
  astro: langEntry(html, "Astro", MARKUP, MARKUP_FORMATTERS),
  sql: streamEntry(standardSQL, "SQL", CLAUSE, SQL_FORMATTERS),
  css: langEntry(cssLang, "CSS", STYLE, WEB_FORMATTERS),
  // SCSS and Less nest rules and carry directives outside `lang-css`, so they use stream modes.
  scss: streamEntry(sCSS, "SCSS", STYLE_WITH_LINE_COMMENTS, WEB_FORMATTERS),
  less: streamEntry(less, "Less", STYLE_WITH_LINE_COMMENTS, WEB_FORMATTERS),
  toml: streamEntry(toml, "TOML", BRACKET, TOML_FORMATTERS),
  ...entriesFor(["al", "perl", "ph", "pl", "plx", "pm", "psgi", "t"], PERL_ENTRY),
  ...entriesFor(["ps1", "psm1", "psd1"], PS_ENTRY),
  hs: streamEntry(haskell, "Haskell", BRACKET, HASKELL_FORMATTERS),
  ...entriesFor(["erl", "hrl"], ERL_ENTRY),
  elm: streamEntry(elm, "Elm", BRACKET),
  ...entriesFor(["groovy", "gvy"], GROOVY_ENTRY),
  gradle: streamEntry(groovy, "Gradle", BRACKET_STATEMENTS, JAVA_FORMATTERS),
  ...entriesFor(["diff", "patch"], DIFF_ENTRY),
  properties: streamEntry(properties, "Properties", NO_LAYOUT),
  ini: streamEntry(properties, "INI", NO_LAYOUT),
  env: streamEntry(properties, "Dotenv", NO_LAYOUT),
  cfg: streamEntry(properties, "Config", NO_LAYOUT),
  cmake: streamEntry(cmake, "CMake", NO_LAYOUT),
  tcl: streamEntry(tcl, "Tcl", NO_LAYOUT),
  cr: streamEntry(crystal, "Crystal", BRACKET),
  d: streamEntry(d, "D", BRACKET_STATEMENTS),
  ...entriesFor(["f", "f90", "f95", "f03"], FORTRAN_ENTRY),
  pas: streamEntry(pascal, "Pascal", NO_LAYOUT),
  dart: streamEntry(dart, "Dart", BRACKET_STATEMENTS, DART_FORMATTERS),
  sass: streamEntry(sass, "Sass", BRACKET),
  php: wordEntry(WORD_LANG_SPECS.php, "PHP", BRACKET_STATEMENTS, PHP_FORMATTERS),
  hack: wordEntry(WORD_LANG_SPECS.hack, "Hack", BRACKET_STATEMENTS, PHP_FORMATTERS),
  ...entriesFor(["ex", "exs"], EX_ENTRY),
  ...entriesFor(["hcl", "nomad"], HCL_ENTRY),
  ...entriesFor(["tf", "tfvars"], TF_ENTRY),
  tofu: wordEntry(WORD_LANG_SPECS.hcl, "OpenTofu", BRACKET_STATEMENTS),
  bzl: langEntry(python, "Starlark", BRACKET),
  sol: wordEntry(WORD_LANG_SPECS.solidity, "Solidity", BRACKET_STATEMENTS, SOLIDITY_FORMATTERS),
  move: wordEntry(WORD_LANG_SPECS.move, "Move", BRACKET_STATEMENTS),
  cairo: wordEntry(WORD_LANG_SPECS.cairo, "Cairo", BRACKET_STATEMENTS),
  circom: wordEntry(WORD_LANG_SPECS.circom, "Circom", BRACKET_STATEMENTS),
  ...entriesFor(["cls", "trigger"], APEX_ENTRY),
  ...entriesFor(["ql", "qll"], QL_ENTRY),
  ...entriesFor(["jsonnet", "libsonnet"], JSONNET_ENTRY),
  promql: wordEntry(WORD_LANG_SPECS.promql, "PromQL", BRACKET),
  ...entriesFor(["log", "out"], LOG_ENTRY),
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

function basenameGroup(ext: CatalogKey, label: string, names: readonly string[]): Record<string, BasenameSpec> {
  const out: Record<string, BasenameSpec> = {};
  for (const name of names) out[name] = { ext, label };
  return out;
}

/** Well-known extensionless, dot-prefixed, or extension-ambiguous filenames. */
const BASENAMES: Record<string, BasenameSpec> = {
  ...basenameGroup("dockerfile", "Dockerfile", ["dockerfile", "containerfile"]),
  "cmakelists.txt": { ext: "cmake", label: "CMake" },
  ".env": { ext: "env", label: "Dotenv" },
  ".gitignore": { ext: "properties", label: "Git ignore" },
  ".gitattributes": { ext: "properties", label: "Git attributes" },
  ".gitmodules": { ext: "properties", label: "Git modules" },
  ".gitconfig": { ext: "properties", label: "Git config" },
  ".editorconfig": { ext: "properties", label: "EditorConfig" },
  ".npmrc": { ext: "properties", label: "npm config" },
  ...basenameGroup("yaml", "YAML", [".clang-format", ".clang-tidy", ".clangd"]),
  ...basenameGroup("rb", "Ruby", ["gemfile", "rakefile", "vagrantfile", "podfile", "fastfile", "brewfile"]),
  jenkinsfile: { ext: "groovy", label: "Groovy" },
  ...basenameGroup("bzl", "Starlark", ["build", "build.bazel", "workspace", "workspace.bazel"]),
  ...basenameGroup("pl", "Perl", [".latexmkrc", "ack", "cpanfile", "latexmkrc", "rexfile"]),
  ...basenameGroup("jsonc", "JSON", ["tsconfig.json", "jsconfig.json", "devcontainer.json", ".devcontainer.json"]),
  ...basenameGroup("json", "JSON", ["composer.lock", "flake.lock", "deno.lock", "pipfile.lock", "package.resolved"]),
  ...basenameGroup("toml", "TOML", ["cargo.lock", "poetry.lock", "uv.lock", "pdm.lock", "pipfile", "gopkg.lock"]),
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
export function languageLabelForDetectedLanguage(language: string, path: string): string {
  const key = language.trim().toLowerCase();
  if (!key) return languageLabelForPath(path);
  if (key === "vimdoc" && extensionOf(basenameOf(path)) === "txt") return PLAIN_TEXT_LABEL;
  const known = DETECTED_LANGUAGE_TO_LABEL[key] ?? catalogEntry(key)?.label;
  return known ?? formatSentenceCase(key);
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
  const lang = StreamLanguage.define(parser);
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
  return (base.startsWith("tsconfig.") || base.startsWith("jsconfig.")) && base.endsWith(".json");
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

/** Formatters whose configuration can set the file's line width, in priority order. */
export function formattersForPath(path: string): readonly FormatTool[] {
  return catalogEntryForPath(path)?.formatters ?? NO_FORMATTERS;
}

export function formattersForBuffer(path: string, detectedLanguage?: string): readonly FormatTool[] {
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
  return (path.trim().replace(/\\/g, "/").split("/").pop() ?? "").toLowerCase();
}

function extensionOf(basename: string): string {
  const i = basename.lastIndexOf(".");
  if (i <= 0 || i === basename.length - 1) return "";
  return basename.slice(i + 1);
}
