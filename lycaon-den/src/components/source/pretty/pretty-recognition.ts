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
import {
  hasCatalogEntryForPath,
  prettyLayoutForLanguage,
  prettyLayoutForPath,
} from "../editor/codemirror-lang.ts";
import { DataLexer, ScriptLexer, StyleLexer } from "./pretty-lexers.ts";
import { NO_LAYOUT, type PrettyLayout } from "./pretty-outcome.ts";
import type { BracketRule, ChainRule, ClauseRule, JsxRule, MarkupRule, PrettyPlan, PrettyRule, PrettySpan, SexpRule } from "./pretty-scan-state.ts";
import { TreeLexer } from "./pretty-tokens.ts";

export const DATA_RULE: BracketRule = {
  kind: "bracket", data: true, statements: false, afterBlock: null, records: false, parsed: false,
  lexer: () => new DataLexer({ singleQuotes: false, comments: false }),
};
const COMMENTED_DATA_RULE: BracketRule = { ...DATA_RULE, lexer: () => new DataLexer({ singleQuotes: true, comments: true }) };
const RECORDS_RULE: BracketRule = { ...DATA_RULE, records: true };
/** Unknown text recognised as JSON-like data, which may quote with `'`. */
export const DATA_LIKE_RULE: BracketRule = { ...DATA_RULE, lexer: () => new DataLexer({ singleQuotes: true, comments: false }) };
export const SCRIPT_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "script", records: false, parsed: false,
  lexer: () => new ScriptLexer(),
};
export const SCRIPT_JSX_RULE: JsxRule = { kind: "jsx" };
export const STYLE_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "style", records: false, parsed: false,
  lexer: () => new StyleLexer(false),
};
export const LINE_COMMENT_STYLE_RULE: BracketRule = { ...STYLE_RULE, lexer: () => new StyleLexer(true) };
export const MARKUP_RULE: MarkupRule = { kind: "markup" };
/** Any brace language: groups over the editor's tokens, with `;` separating statements where the language has them. */
const BRACKET_RULE: BracketRule = {
  kind: "bracket", data: false, statements: false, afterBlock: null, records: false, parsed: true,
  lexer: () => new TreeLexer(),
};
const BRACKET_BLOCKS_RULE: BracketRule = { ...BRACKET_RULE, afterBlock: "block" };
const BRACKET_STATEMENTS_RULE: BracketRule = { ...BRACKET_RULE, statements: true, afterBlock: "block" };
const CLAUSE_RULE: ClauseRule = { kind: "clause" };
const SEXP_RULE: SexpRule = { kind: "sexp", data: false };
const SEXP_DATA_RULE: SexpRule = { kind: "sexp", data: true };
const CHAIN_RULE: ChainRule = { kind: "chain" };

/** The rule that applies a declared layout, or null where no rule exists. */
function ruleFor(layout: PrettyLayout): PrettyRule | null {
  switch (layout.kind) {
    case "data": return layout.records ? RECORDS_RULE : layout.comments ? COMMENTED_DATA_RULE : DATA_RULE;
    case "markup": return MARKUP_RULE;
    case "code":
      return layout.dialect === "script"
        ? SCRIPT_RULE
        : layout.dialect === "script-jsx"
          ? SCRIPT_JSX_RULE
          : layout.dialect === "style"
            ? STYLE_RULE
            : LINE_COMMENT_STYLE_RULE;
    case "bracket": return layout.statements ? BRACKET_STATEMENTS_RULE : layout.blocks ? BRACKET_BLOCKS_RULE : BRACKET_RULE;
    case "clause": return CLAUSE_RULE;
    case "sexp": return layout.data ? SEXP_DATA_RULE : SEXP_RULE;
    case "chain": return CHAIN_RULE;
    case "fenced":
    case "containers":
    case "none":
      return null;
  }
}

function fenceRule(info: string): PrettyRule | null {
  return ruleFor(prettyLayoutForLanguage(info));
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
  const hint = languageHint?.trim();
  if (!hint && !path) return UNKNOWN;
  const hintedLayout = hint ? prettyLayoutForLanguage(hint) : null;
  const layout =
    hintedLayout && hintedLayout.kind !== "none"
      ? hintedLayout
      : path
        ? prettyLayoutForPath(path)
        : NO_LAYOUT;
  if (layout.kind === "fenced") return FENCED;
  if (layout.kind === "containers") return UNKNOWN;
  const rule = ruleFor(layout);
  if (rule) return { kind: "document", rule };
  if (!hint && path && !hasCatalogEntryForPath(path)) return UNKNOWN;
  return null;
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

function skipBlanks(text: string, from: number): number {
  while (from < text.length && (text.charCodeAt(from) === 0x20 || text.charCodeAt(from) === 0x09)) from += 1;
  return from;
}

/** Where a container may start on a line: at its start, or after host markers or a log prefix. */
export function containerStartColumn(lineText: string): number | null {
  let cursor = skipBlanks(lineText, 0);
  const first = lineText.charCodeAt(cursor);
  if (first === 0x7b || first === 0x5b) return cursor;
  let marked = false;
  for (;;) {
    const length = hostMarkerLength(lineText.slice(cursor));
    if (length === 0) break;
    marked = true;
    cursor += length;
    cursor = skipBlanks(lineText, cursor);
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
// JSON literals, and Python's, so a printed dict or list is recognised too.
const LITERAL = /^(?:true|false|null|True|False|None)\b/;

/**
 * Reads a JSON container starting at `from`. Returns its end, or the negated
 * position of the first character that is not a JSON token.
 */
export function readJsonContainer(text: string, from: number): number {
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
    } else if (c === 0x22 || c === 0x27) {
      i += 1;
      while (i < text.length) {
        const s = text.charCodeAt(i);
        if (s === 0x5c) i += 2;
        else if (s === c) break;
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

/** Balanced JSON-like containers at line starts or after host markers and log prefixes. */
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
    let pos = line.from + column;
    let nextLine = lineNumber + 1;
    while (pos < line.to) {
      const ch = text.charCodeAt(pos);
      if (ch !== 0x7b && ch !== 0x5b) {
        pos += 1;
        continue;
      }
      const end = readJsonContainer(text, pos);
      if (end > 0) {
        spans.push({ from: pos, to: end, rule: DATA_LIKE_RULE });
        if (end > line.to) {
          nextLine = doc.lineAt(end).number + 1;
          break;
        }
        pos = end;
        continue;
      }
      const failLine = doc.lineAt(Math.min(doc.length, -end)).number;
      if (failLine > lineNumber) {
        nextLine = Math.max(nextLine, failLine);
        break;
      }
      pos = Math.max(pos + 1, -end);
    }
    lineNumber = nextLine;
  }
  return spans;
}
