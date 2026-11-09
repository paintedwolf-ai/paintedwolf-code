import {
  CODE_LINE,
  COMPACTION_BANNER_CLOSE,
  COMPACTION_BANNER_OPEN,
  EVIDENCE_COVERAGE_PATTERN,
  EVIDENCE_HANDLE_PATTERN,
  GUIDANCE_BLOCK_OPEN,
  OVERLAY_PROMOTE_EVENT_PREFIX,
  OVERLAY_REJECT_EVENT_PREFIX,
  REJECTED,
  VERBATIM_HEAD_TAIL,
} from "../../../chat/host-markers.generated.ts";

export type PrettyLayoutFamily =
  | "json"
  | "jsonl"
  | "jsonc"
  | "xml-html"
  | "javascript"
  | "css"
  | "markdown"
  | "mixed"
  | "passthrough";

export type DocumentFormatMode =
  | "code-passthrough"
  | "json"
  | "jsonl"
  | "jsonc"
  | "xml-html"
  | "javascript"
  | "css"
  | "markdown"
  | "auto";

/**
 * Whitespace-sensitive and unimplemented CodeMirror languages remain truthful native
 * source presentation (`code-passthrough`) without claiming structural prettification
 * (Decision 005, FMT-AC-04, FMT-AC-20).
 */
const PASSTHROUGH_CODE_EXTENSIONS = new Set([
  "go", "py", "pyi", "rs", "sass",
  "sh", "bash", "zsh", "fish", "ps1", "psm1", "psd1",
  "yaml", "yml", "toml", "ini", "cfg", "conf", "properties", "env",
  "sql", "graphql", "gql", "proto",
  "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "hxx", "cs",
  "java", "kt", "kts", "scala", "swift", "rb", "php", "hack",
  "pl", "pm", "perl", "lua", "r", "jl", "zig", "nim",
  "ex", "exs", "erl", "hrl", "hs", "elm", "ml", "mli", "fs", "fsx",
  "clj", "cljs", "cljc", "edn", "scm", "ss", "lisp", "cl", "lsp",
  "dart", "dockerfile", "mk", "cmake", "csv", "tsv", "diff", "patch",
  "tf", "tfvars", "hcl", "nix", "sol", "move", "cairo", "circom",
  "cls", "trigger", "ql", "qll", "promql", "groovy", "gradle",
  "tcl", "cr", "d", "f", "f90", "f95", "f03", "pas", "vim", "el",
]);

const PASSTHROUGH_CODE_BASENAMES = new Set([
  "dockerfile", "containerfile", "makefile", "gnumakefile", "cmakelists.txt",
  "justfile", "procfile", "cargo.lock", "go.mod", "go.sum",
  ".gitignore", ".gitattributes", ".gitmodules", ".gitconfig",
  ".editorconfig", ".npmrc", ".env", "gemfile", "rakefile", "vagrantfile",
]);

const PASSTHROUGH_CODE_HINTS = new Set([
  ...PASSTHROUGH_CODE_EXTENSIONS,
  "golang", "python", "rust", "shell", "csharp", "c_sharp", "kotlin",
  "ruby", "makefile", "dockerfile", "powershell", "haskell", "erlang",
  "ocaml", "commonlisp", "scheme", "clojure", "julia", "fortran", "pascal",
  "solidity", "terraform", "apex", "codeql", "elixir",
]);

const JSON_EXTENSIONS = new Set(["json", "webmanifest", "har", "avsc"]);
const JSON_HINTS = new Set(["json", "application/json"]);

const JSONL_EXTENSIONS = new Set(["jsonl", "ndjson"]);
const JSONL_HINTS = new Set(["jsonl", "ndjson", "application/x-ndjson"]);

const JSONC_EXTENSIONS = new Set(["jsonc", "json5", "jsonnet", "libsonnet"]);
const JSONC_HINTS = new Set([
  "jsonc",
  "json5",
  "jsonnet",
  "libsonnet",
  "application/jsonc",
  "application/json5",
]);

const XML_HTML_EXTENSIONS = new Set([
  "xml",
  "svg",
  "xhtml",
  "html",
  "htm",
  "vue",
  "svelte",
  "astro",
]);
const XML_HTML_HINTS = new Set([
  ...XML_HTML_EXTENSIONS,
  "application/xml",
  "text/xml",
  "text/html",
  "image/svg+xml",
]);

const JAVASCRIPT_EXTENSIONS = new Set([
  "js",
  "jsx",
  "mjs",
  "cjs",
  "ts",
  "tsx",
  "mts",
  "cts",
]);
const JAVASCRIPT_HINTS = new Set([
  ...JAVASCRIPT_EXTENSIONS,
  "javascript",
  "typescript",
  "application/javascript",
  "text/javascript",
]);

const CSS_EXTENSIONS = new Set(["css", "scss", "less"]);
const CSS_HINTS = new Set(["css", "scss", "less", "text/css"]);

const MARKDOWN_EXTENSIONS = new Set(["md", "markdown", "mdx"]);
const MARKDOWN_HINTS = new Set(["md", "markdown", "mdx", "gfm", "text/markdown"]);

const EVIDENCE_TAG_REGEX = new RegExp(
  `^\\[(?:${EVIDENCE_HANDLE_PATTERN})(?: ${EVIDENCE_COVERAGE_PATTERN})?\\]`,
);

const LOG_TIMESTAMP_PREFIX_REGEX =
  /^(?:\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?|\d{2}:\d{2}:\d{2}(?:\.\d+)?)\b(?:[^={\[\n`<>]|\[\w[A-Za-z0-9_.:/# -]*\])+(?=\{|\[)/;

const LOG_LEVEL_OR_LABEL_PREFIX_REGEX =
  /^(?:(?:INFO|WARN|WARNING|ERROR|DEBUG|TRACE|FATAL)\b(?:[^={\[\n`<>]|\[\w[A-Za-z0-9_.:/# -]*\])+|[A-Za-z][A-Za-z0-9 _.-]{0,32}:\s+)(?=\{|\[)/;

const BRACKET_TAG_CHAIN_REGEX = /^(?:\[[A-Za-z0-9_.:/# -]+\]\s+)+(?=\{|\[)/;

function extractNormalizedExtension(path: string): {
  basename: string;
  ext: string;
} {
  const clean = path.split(/[?#]/)[0] ?? path;
  const slashIdx = Math.max(clean.lastIndexOf("/"), clean.lastIndexOf("\\"));
  const basename = (slashIdx >= 0 ? clean.slice(slashIdx + 1) : clean).toLowerCase();
  const dotIdx = basename.lastIndexOf(".");
  const ext = dotIdx > 0 ? basename.slice(dotIdx + 1) : "";
  return { basename, ext };
}

export function isKnownCodeOrMarkupPath(
  path?: string,
  languageHint?: string,
): boolean {
  const mode = classifyDocumentFormat("", path, languageHint);
  return mode === "code-passthrough";
}

function looksLikePassthroughCodeWithoutHint(rawText: string): boolean {
  const trimmed = rawText.trimStart();
  if (!trimmed) return false;
  if (/^(?:```|~~~)/m.test(rawText)) return false;
  if (
    /^(?:package\s+[a-zA-Z_]|import\s+(?:["({*]|[a-zA-Z_])|export\s+(?:default|const|function|class|type|interface)|func\s+[a-zA-Z_(]|def\s+[a-zA-Z_]|class\s+[a-zA-Z_]|#include\s+[<"])/i.test(
      trimmed,
    )
  ) {
    return true;
  }
  return false;
}

export function classifyDocumentFormat(
  rawText: string,
  path?: string,
  languageHint?: string,
): DocumentFormatMode {
  if (languageHint) {
    const hint = languageHint.trim().toLowerCase();
    if (PASSTHROUGH_CODE_HINTS.has(hint)) return "code-passthrough";
    if (JSON_HINTS.has(hint)) return "json";
    if (JSONL_HINTS.has(hint)) return "jsonl";
    if (JSONC_HINTS.has(hint)) return "jsonc";
    if (XML_HTML_HINTS.has(hint)) return "xml-html";
    if (JAVASCRIPT_HINTS.has(hint)) return "javascript";
    if (CSS_HINTS.has(hint)) return "css";
    if (MARKDOWN_HINTS.has(hint)) return "markdown";
  }
  if (path) {
    const { basename, ext } = extractNormalizedExtension(path);
    if (PASSTHROUGH_CODE_BASENAMES.has(basename)) return "code-passthrough";
    if (ext) {
      if (PASSTHROUGH_CODE_EXTENSIONS.has(ext)) return "code-passthrough";
      if (JSON_EXTENSIONS.has(ext)) return "json";
      if (JSONL_EXTENSIONS.has(ext)) return "jsonl";
      if (JSONC_EXTENSIONS.has(ext)) return "jsonc";
      if (XML_HTML_EXTENSIONS.has(ext)) return "xml-html";
      if (JAVASCRIPT_EXTENSIONS.has(ext)) return "javascript";
      if (CSS_EXTENSIONS.has(ext)) return "css";
      if (MARKDOWN_EXTENSIONS.has(ext)) return "markdown";
    }
  }
  if (looksLikePassthroughCodeWithoutHint(rawText)) {
    return "code-passthrough";
  }
  return "auto";
}

function consumeSingleHostMarkerPrefix(rest: string): number {
  const tagMatch = EVIDENCE_TAG_REGEX.exec(rest);
  if (tagMatch) return tagMatch[0].length;
  if (rest.startsWith(OVERLAY_PROMOTE_EVENT_PREFIX)) {
    return OVERLAY_PROMOTE_EVENT_PREFIX.length;
  }
  if (rest.startsWith(OVERLAY_REJECT_EVENT_PREFIX)) {
    return OVERLAY_REJECT_EVENT_PREFIX.length;
  }
  if (rest.startsWith(COMPACTION_BANNER_OPEN)) {
    const closeIdx = rest.indexOf(COMPACTION_BANNER_CLOSE);
    if (closeIdx >= 0) return closeIdx + COMPACTION_BANNER_CLOSE.length;
  }
  if (rest.startsWith(VERBATIM_HEAD_TAIL)) {
    return VERBATIM_HEAD_TAIL.length;
  }
  if (rest.startsWith(GUIDANCE_BLOCK_OPEN)) {
    return GUIDANCE_BLOCK_OPEN.length;
  }
  if (rest.startsWith(REJECTED)) {
    return REJECTED.length;
  }
  if (rest.startsWith(CODE_LINE)) {
    return CODE_LINE.length;
  }
  return 0;
}

/**
 * If `lineText` starts with one or more host markers or log/envelope prefixes
 * followed by `{` or `[`, returns the character offset of that `{` or `[`.
 */
export function matchEnvelopeOrLogPrefix(lineText: string): number | null {
  let cursor = 0;
  while (
    cursor < lineText.length &&
    (lineText.charCodeAt(cursor) === 0x20 || lineText.charCodeAt(cursor) === 0x09)
  ) {
    cursor += 1;
  }

  let matchedMarker = false;
  while (cursor < lineText.length) {
    const consumed = consumeSingleHostMarkerPrefix(lineText.slice(cursor));
    if (consumed <= 0) break;
    matchedMarker = true;
    cursor += consumed;
    while (
      cursor < lineText.length &&
      (lineText.charCodeAt(cursor) === 0x20 || lineText.charCodeAt(cursor) === 0x09)
    ) {
      cursor += 1;
    }
  }

  if (matchedMarker) {
    const ch = lineText.charCodeAt(cursor);
    return ch === 0x7b || ch === 0x5b ? cursor : null;
  }

  const sub = lineText.slice(cursor);
  const tsMatch = LOG_TIMESTAMP_PREFIX_REGEX.exec(sub);
  if (tsMatch) return cursor + tsMatch[0].length;

  const levelMatch = LOG_LEVEL_OR_LABEL_PREFIX_REGEX.exec(sub);
  if (levelMatch) return cursor + levelMatch[0].length;

  const bracketMatch = BRACKET_TAG_CHAIN_REGEX.exec(sub);
  if (bracketMatch) return cursor + bracketMatch[0].length;

  return null;
}

/** True when a line is a standalone host marker, compaction banner, or receipt header line. */
export function isEnvelopeHeaderLine(lineText: string): boolean {
  const trimmed = lineText.trim();
  if (!trimmed) return false;
  if (consumeSingleHostMarkerPrefix(trimmed) > 0) return true;
  if (trimmed.startsWith("⟦D")) return true;
  if (/^[A-Za-z][A-Za-z0-9 _-]{0,32}:\s*\S*$/.test(trimmed) && !/[=;`<>]/.test(trimmed)) {
    return true;
  }
  return false;
}

/** True when a line cannot be crossed by a multi-line JSON body in unknown/envelope mode. */
export function isExcerptBarrierLine(lineText: string): boolean {
  const trimmed = lineText.trim();
  if (!trimmed) return false;
  if (trimmed === "..." || trimmed === "…") return true;
  if (consumeSingleHostMarkerPrefix(trimmed) > 0) return true;
  if (trimmed.startsWith("⟦D")) return true;
  return false;
}

export interface LineSpan {
  readonly lineFrom: number;
  readonly lineTo: number;
  readonly sepFrom: number;
  readonly sepTo: number;
}

export function splitLines(text: string, from = 0, to = text.length): LineSpan[] {
  const lines: LineSpan[] = [];
  let lineStart = from;
  for (let i = from; i < to; i++) {
    if (text.charCodeAt(i) === 0x0a) {
      const hasCr = i > lineStart && text.charCodeAt(i - 1) === 0x0d;
      const lineTo = hasCr ? i - 1 : i;
      lines.push({ lineFrom: lineStart, lineTo, sepFrom: lineTo, sepTo: i + 1 });
      lineStart = i + 1;
    }
  }
  if (lineStart <= to && (lines.length === 0 || lineStart < to)) {
    lines.push({ lineFrom: lineStart, lineTo: to, sepFrom: to, sepTo: to });
  }
  return lines;
}

export function parseOpeningFence(lineText: string): { char: string; len: number; lang: string } | null {
  const m = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(lineText);
  if (!m) return null;
  const fence = m[1]!;
  const info = m[2] ?? "";
  const char = fence[0]!;
  if (char === "`" && info.includes("`")) return null;
  const lang = (info.trim().split(/\s+/)[0] ?? "").toLowerCase();
  return { char, len: fence.length, lang };
}

export function isClosingFence(lineText: string, char: string, minLen: number): boolean {
  const m = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lineText);
  if (!m) return false;
  const fence = m[1]!;
  return fence[0] === char && fence.length >= minLen;
}

export function isJsonFenceLang(lang: string): "json" | "jsonl" | null {
  if (lang === "json" || lang === "application/json") return "json";
  if (lang === "jsonl" || lang === "ndjson" || lang === "application/x-ndjson") return "jsonl";
  return null;
}

/**
 * Resolves a Markdown fence info string to a supported common-blob family.
 * Unsupported languages return `null` so fenced code in those languages remains raw.
 */
export function resolveFenceLayoutFamily(lang: string): PrettyLayoutFamily | null {
  const normalized = lang.replace(/^\./, "").toLowerCase();
  if (!normalized) return null;
  if (JSON_HINTS.has(normalized) || JSON_EXTENSIONS.has(normalized)) return "json";
  if (JSONL_HINTS.has(normalized) || JSONL_EXTENSIONS.has(normalized)) return "jsonl";
  if (JSONC_HINTS.has(normalized) || JSONC_EXTENSIONS.has(normalized)) return "jsonc";
  if (XML_HTML_HINTS.has(normalized) || XML_HTML_EXTENSIONS.has(normalized)) return "xml-html";
  if (JAVASCRIPT_HINTS.has(normalized) || JAVASCRIPT_EXTENSIONS.has(normalized)) return "javascript";
  if (CSS_HINTS.has(normalized) || CSS_EXTENSIONS.has(normalized)) return "css";
  return null;
}
