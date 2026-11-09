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

export type DocumentFormatMode =
  | "code-passthrough"
  | "json"
  | "jsonl"
  | "markdown"
  | "auto";

const CODE_OR_MARKUP_EXTENSIONS = new Set([
  "go", "ts", "tsx", "mts", "cts", "js", "jsx", "mjs", "cjs", "py", "pyi", "rs",
  "html", "htm", "xhtml", "vue", "svelte", "astro", "css", "scss", "sass", "less",
  "sh", "bash", "zsh", "fish", "ps1", "yaml", "yml", "toml", "ini", "cfg", "conf",
  "sql", "graphql", "gql", "proto", "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "cs",
  "java", "kt", "kts", "scala", "swift", "rb", "php", "pl", "pm", "lua", "r", "zig",
  "nim", "ex", "exs", "erl", "hrl", "hs", "ml", "mli", "fs", "fsx", "clj", "cljs",
  "dart", "dockerfile", "mk", "cmake", "xml", "svg", "csv", "tsv", "diff", "patch",
  "tf", "hcl", "nix", "sol", "vim", "el",
]);

const CODE_OR_MARKUP_BASENAMES = new Set([
  "dockerfile", "makefile", "gnumakefile", "cmakelists.txt", "justfile", "procfile",
  "cargo.lock", "go.mod", "go.sum", ".gitignore", ".gitattributes", ".editorconfig", ".env",
]);

const CODE_OR_MARKUP_HINTS = new Set([
  ...CODE_OR_MARKUP_EXTENSIONS,
  "golang", "typescript", "javascript", "python", "rust", "shell", "csharp", "kotlin", "ruby", "makefile",
]);

const JSON_EXTENSIONS = new Set(["json", "webmanifest", "har", "avsc"]);
const JSON_HINTS = new Set(["json", "application/json"]);
const JSONL_EXTENSIONS = new Set(["jsonl", "ndjson"]);
const JSONL_HINTS = new Set(["jsonl", "ndjson", "application/x-ndjson"]);
const MARKDOWN_EXTENSIONS = new Set(["md", "markdown", "mdx"]);
const MARKDOWN_HINTS = new Set(["md", "markdown", "gfm"]);

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
  if (languageHint) {
    const hint = languageHint.trim().toLowerCase();
    if (CODE_OR_MARKUP_HINTS.has(hint)) return true;
    if (
      JSON_HINTS.has(hint) ||
      JSONL_HINTS.has(hint) ||
      MARKDOWN_HINTS.has(hint)
    ) {
      return false;
    }
  }
  if (path) {
    const { basename, ext } = extractNormalizedExtension(path);
    if (CODE_OR_MARKUP_BASENAMES.has(basename)) return true;
    if (ext && CODE_OR_MARKUP_EXTENSIONS.has(ext)) return true;
  }
  return false;
}

function looksLikeCodeOrMarkupWithoutHint(rawText: string): boolean {
  const trimmed = rawText.trimStart();
  if (!trimmed) return false;
  if (/^(?:```|~~~)/m.test(rawText)) return false;
  if (
    /^(?:package\s+[a-zA-Z_]|import\s+(?:["({*]|[a-zA-Z_])|export\s+(?:default|const|function|class|type|interface)|func\s+[a-zA-Z_(]|def\s+[a-zA-Z_]|class\s+[a-zA-Z_]|#include\s+[<"]|<!DOCTYPE\s+html|<html[\s>]|<div[\s>]|<span[\s>]|<script[\s>]|<style[\s>])/i.test(
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
    if (CODE_OR_MARKUP_HINTS.has(hint)) return "code-passthrough";
    if (JSON_HINTS.has(hint)) return "json";
    if (JSONL_HINTS.has(hint)) return "jsonl";
    if (MARKDOWN_HINTS.has(hint)) return "markdown";
  }
  if (path) {
    const { basename, ext } = extractNormalizedExtension(path);
    if (CODE_OR_MARKUP_BASENAMES.has(basename)) return "code-passthrough";
    if (ext) {
      if (CODE_OR_MARKUP_EXTENSIONS.has(ext)) return "code-passthrough";
      if (JSON_EXTENSIONS.has(ext)) return "json";
      if (JSONL_EXTENSIONS.has(ext)) return "jsonl";
      if (MARKDOWN_EXTENSIONS.has(ext)) return "markdown";
    }
  }
  if (looksLikeCodeOrMarkupWithoutHint(rawText)) {
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

