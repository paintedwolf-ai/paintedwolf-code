import type { Text } from "@codemirror/state";
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
import { DataLexer, ScriptLexer, StyleLexer } from "./pretty-lexers.ts";
import type { BracketRule, MarkupRule, PrettyPlan, PrettyRule, PrettySpan } from "./pretty-scan-state.ts";

const plainData = { singleQuotes: false, comments: false };
const commentedData = { singleQuotes: true, comments: true };

export const DATA_RULE: BracketRule = {
  kind: "bracket", data: true, statements: false, afterBlock: null, records: false,
  lexer: () => new DataLexer(plainData),
};
const COMMENTED_DATA_RULE: BracketRule = { ...DATA_RULE, lexer: () => new DataLexer(commentedData) };
const RECORDS_RULE: BracketRule = { ...DATA_RULE, records: true };
export const SCRIPT_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "script", records: false,
  lexer: () => new ScriptLexer(),
};
export const STYLE_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "style", records: false,
  lexer: () => new StyleLexer(false),
};
const PREPROCESSED_STYLE_RULE: BracketRule = { ...STYLE_RULE, lexer: () => new StyleLexer(true) };
export const MARKUP_RULE: MarkupRule = { kind: "markup" };

const RULES_BY_NAME = new Map<string, PrettyRule>([
  ...["json", "webmanifest", "har", "avsc", "application/json"].map((name) => [name, DATA_RULE] as const),
  ...["jsonc", "json5", "jsonnet", "libsonnet", "application/jsonc", "application/json5"].map((name) => [name, COMMENTED_DATA_RULE] as const),
  ...["jsonl", "ndjson", "application/x-ndjson"].map((name) => [name, RECORDS_RULE] as const),
  ...["js", "jsx", "mjs", "cjs", "ts", "tsx", "mts", "cts", "javascript", "typescript", "application/javascript", "text/javascript"]
    .map((name) => [name, SCRIPT_RULE] as const),
  ...["css", "text/css"].map((name) => [name, STYLE_RULE] as const),
  ...["scss", "less"].map((name) => [name, PREPROCESSED_STYLE_RULE] as const),
  ...["xml", "svg", "xhtml", "html", "htm", "vue", "svelte", "astro", "application/xml", "text/xml", "text/html", "image/svg+xml"]
    .map((name) => [name, MARKUP_RULE] as const),
]);
const MARKDOWN_NAMES = new Set(["md", "markdown", "mdx", "gfm", "text/markdown"]);

function extensionOf(path: string): string {
  const clean = path.split(/[?#]/)[0] ?? path;
  const slash = Math.max(clean.lastIndexOf("/"), clean.lastIndexOf("\\"));
  const base = (slash >= 0 ? clean.slice(slash + 1) : clean).toLowerCase();
  const dot = base.lastIndexOf(".");
  return dot > 0 ? base.slice(dot + 1) : "";
}

function fenceRule(info: string): PrettyRule | null {
  return RULES_BY_NAME.get(info.replace(/^\./, "")) ?? null;
}

/** What decides a document's layout: its language, Markdown fences, or recognition of unknown text. */
export type PrettyTarget =
  | { readonly kind: "document"; readonly rule: PrettyRule }
  | { readonly kind: "fenced" }
  | { readonly kind: "unknown" };

const FENCED: PrettyTarget = { kind: "fenced" };
const UNKNOWN: PrettyTarget = { kind: "unknown" };

/**
 * A known language or path decides the layout; text with neither is laid out
 * only where JSON containers are positively identified. Returns null for a
 * language without a layout.
 */
export function prettyTargetFor(path?: string, languageHint?: string): PrettyTarget | null {
  const name = languageHint?.trim().toLowerCase() || (path ? extensionOf(path) : "");
  if (name) {
    if (MARKDOWN_NAMES.has(name)) return FENCED;
    const rule = RULES_BY_NAME.get(name);
    return rule ? { kind: "document", rule } : null;
  }
  return path ? null : UNKNOWN;
}

export function prettyPlanFor(doc: Text, target: PrettyTarget): PrettyPlan {
  switch (target.kind) {
    case "document": return { kind: "document", rule: target.rule };
    case "fenced": return { kind: "fenced", fenceRule };
    case "unknown": return { kind: "spans", spans: recognizeJsonContainers(doc) };
  }
}

const EVIDENCE_TAG = new RegExp(`^\\[(?:${EVIDENCE_HANDLE_PATTERN})(?: ${EVIDENCE_COVERAGE_PATTERN})?\\]`);
const LOG_TIMESTAMP_PREFIX =
  /^(?:\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?|\d{2}:\d{2}:\d{2}(?:\.\d+)?)\b(?:[^={[\n`<>]|\[\w[A-Za-z0-9_.:/# -]*\])+(?=\{|\[)/;
const LOG_LEVEL_OR_LABEL_PREFIX =
  /^(?:(?:INFO|WARN|WARNING|ERROR|DEBUG|TRACE|FATAL)\b(?:[^={[\n`<>]|\[\w[A-Za-z0-9_.:/# -]*\])+|[A-Za-z][A-Za-z0-9 _.-]{0,32}:\s+)(?=\{|\[)/;
const BRACKET_TAG_CHAIN = /^(?:\[[A-Za-z0-9_.:/# -]+\]\s+)+(?=\{|\[)/;

function hostMarkerLength(rest: string): number {
  const tag = EVIDENCE_TAG.exec(rest);
  if (tag) return tag[0].length;
  for (const marker of [OVERLAY_PROMOTE_EVENT_PREFIX, OVERLAY_REJECT_EVENT_PREFIX, VERBATIM_HEAD_TAIL, GUIDANCE_BLOCK_OPEN, REJECTED, CODE_LINE]) {
    if (rest.startsWith(marker)) return marker.length;
  }
  if (rest.startsWith(COMPACTION_BANNER_OPEN)) {
    const close = rest.indexOf(COMPACTION_BANNER_CLOSE);
    if (close >= 0) return close + COMPACTION_BANNER_CLOSE.length;
  }
  return 0;
}

/** Where a container may start on a line: at its start, or after host markers or a log prefix. */
export function containerStartColumn(lineText: string): number | null {
  let cursor = /^[ \t]*/.exec(lineText)![0].length;
  const first = lineText.charCodeAt(cursor);
  if (first === 0x7b || first === 0x5b) return cursor;
  let marked = false;
  for (;;) {
    const length = hostMarkerLength(lineText.slice(cursor));
    if (length === 0) break;
    marked = true;
    cursor += length;
    cursor += /^[ \t]*/.exec(lineText.slice(cursor))![0].length;
  }
  if (marked) {
    const c = lineText.charCodeAt(cursor);
    return c === 0x7b || c === 0x5b ? cursor : null;
  }
  const rest = lineText.slice(cursor);
  for (const prefix of [LOG_TIMESTAMP_PREFIX, LOG_LEVEL_OR_LABEL_PREFIX, BRACKET_TAG_CHAIN]) {
    const match = prefix.exec(rest);
    if (match) return cursor + match[0].length;
  }
  return null;
}

const NUMBER = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/;
const LITERAL = /^(?:true|false|null)/;

/**
 * Reads a JSON container starting at `from`. Returns its end, or the negated
 * position of the first character that is not a JSON token.
 */
function readJsonContainer(text: string, from: number): number {
  const stack: number[] = [];
  let i = from;
  while (i < text.length) {
    const c = text.charCodeAt(i);
    if (c === 0x20 || c === 0x09 || c === 0x0a || c === 0x0d || c === 0x3a || c === 0x2c) {
      i += 1;
    } else if (c === 0x7b || c === 0x5b) {
      stack.push(c === 0x7b ? 0x7d : 0x5d);
      i += 1;
    } else if (c === 0x7d || c === 0x5d) {
      if (stack.pop() !== c) return -i;
      i += 1;
      if (stack.length === 0) return i;
    } else if (c === 0x22) {
      i += 1;
      while (i < text.length) {
        const s = text.charCodeAt(i);
        if (s === 0x5c) i += 2;
        else if (s === 0x22) break;
        else if (s === 0x0a) return -i;
        else i += 1;
      }
      if (i >= text.length) return -i;
      i += 1;
    } else {
      const rest = text.slice(i, i + 32);
      const token = NUMBER.exec(rest) ?? LITERAL.exec(rest);
      if (!token) return -i;
      i += token[0].length;
    }
  }
  return -i;
}

/** Balanced JSON containers at line starts or after host markers and log prefixes. */
export function recognizeJsonContainers(doc: Text): PrettySpan[] {
  const text = doc.toString();
  const spans: PrettySpan[] = [];
  let lineNumber = 1;
  while (lineNumber <= doc.lines) {
    const line = doc.line(lineNumber);
    const column = containerStartColumn(line.text);
    if (column === null) {
      lineNumber += 1;
      continue;
    }
    const end = readJsonContainer(text, line.from + column);
    if (end > 0) {
      spans.push({ from: line.from + column, to: end, rule: DATA_RULE });
      lineNumber = doc.lineAt(end).number + 1;
    } else {
      // Lines already read as part of a failed candidate are not tried again.
      lineNumber = Math.max(lineNumber + 1, doc.lineAt(Math.min(doc.length, -end)).number);
    }
  }
  return spans;
}
