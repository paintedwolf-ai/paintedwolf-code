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
import type {
  BracketRule,
  ChainRule,
  ClauseRule,
  JsxRule,
  MarkupRule,
  PrettyPlan,
  PrettyRule,
  PrettySpan,
  SexpRule,
} from "./pretty-scan-state.ts";
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
  lexer: () => new ScriptLexer(false),
};
const SCRIPT_JSX_RULE: JsxRule = { kind: "jsx" };
export const STYLE_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "style", records: false, parsed: false,
  lexer: () => new StyleLexer(false),
};
const LINE_COMMENT_STYLE_RULE: BracketRule = { ...STYLE_RULE, lexer: () => new StyleLexer(true) };
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
type PrettyTarget =
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
  /^(?:\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)?|\d{2}:\d{2}:\d{2}(?:\.\d+)?)\b(?:[^{[\n`<>]|\[\w[A-Za-z0-9_.:/# -]*\])+(?=\{|\[)/;
const LOG_LEVEL_OR_LABEL_PREFIX =
  /^(?:(?:INFO|WARN|WARNING|ERROR|DEBUG|TRACE|FATAL)\b(?:[^{[\n`<>]|\[\w[A-Za-z0-9_.:/# -]*\])+|[A-Za-z][A-Za-z0-9 _.-]{0,32}:\s+)(?=\{|\[)/;
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

const enum JsonStep {
  ObjFirstKeyOrClose,
  ObjKey,
  ObjColon,
  ObjValue,
  ObjCommaOrClose,
  ArrFirstValOrClose,
  ArrValue,
  ArrCommaOrClose,
}

function skipJsonString(text: string, from: number, quote: number): number {
  let i = from + 1;
  while (i < text.length) {
    const s = text.charCodeAt(i);
    if (s === 0x5c) {
      if (i + 1 >= text.length) return -text.length;
      const esc = text.charCodeAt(i + 1);
      if (esc === 0x0a || esc === 0x0d) return -(i + 1);
      i += 2;
    } else if (s === quote) {
      return i + 1;
    } else if (s === 0x0a || s === 0x0d) {
      return -i;
    } else {
      i += 1;
    }
  }
  return -text.length;
}

function readScalarValue(text: string, i: number): number {
  const c = text.charCodeAt(i);
  if (c === 0x22 || c === 0x27) return skipJsonString(text, i, c);
  const rest = text.slice(i, i + 32);
  const token = NUMBER.exec(rest) ?? LITERAL.exec(rest);
  if (!token) return -i;
  const end = i + token[0].length;
  if (end < text.length) {
    const next = text.charCodeAt(end);
    if ((next >= 0x30 && next <= 0x39) || (next >= 0x41 && next <= 0x5a) || (next >= 0x61 && next <= 0x7a) || next === 0x5f) {
      return -end;
    }
  }
  return end;
}

/**
 * Reads a grammatical JSON or Python-dict container starting at `from`.
 * Returns its end, or the negated offset of the first token that breaks the grammar.
 */
export function readJsonContainer(
  text: string,
  from: number,
  nested?: Array<{ from: number; to: number }>,
): number {
  if (from >= text.length) return -from;
  const first = text.charCodeAt(from);
  if (first !== 0x7b && first !== 0x5b) return -from;
  const stack: JsonStep[] = [first === 0x7b ? JsonStep.ObjFirstKeyOrClose : JsonStep.ArrFirstValOrClose];
  const openOffsets: number[] = [from];
  const closeFrame = (at: number): number => {
    stack.pop();
    const start = openOffsets.pop() ?? from;
    if (stack.length === 1 && nested) nested.push({ from: start, to: at });
    return stack.length;
  };
  let pythonic = false;
  let i = from + 1;
  while (i < text.length) {
    const c = text.charCodeAt(i);
    if (c === 0x20 || c === 0x09 || c === 0x0a || c === 0x0d) {
      i += 1;
      continue;
    }
    const top = stack[stack.length - 1]!;
    switch (top) {
      case JsonStep.ObjFirstKeyOrClose:
      case JsonStep.ObjKey: {
        if ((top === JsonStep.ObjFirstKeyOrClose || pythonic) && c === 0x7d) {
          i += 1;
          if (closeFrame(i) === 0) return i;
          continue;
        }
        if (c !== 0x22 && c !== 0x27) return -i;
        if (c === 0x27) pythonic = true;
        const end = skipJsonString(text, i, c);
        if (end < 0) return end;
        stack[stack.length - 1] = JsonStep.ObjColon;
        i = end;
        continue;
      }
      case JsonStep.ObjColon:
        if (c !== 0x3a) return -i;
        stack[stack.length - 1] = JsonStep.ObjValue;
        i += 1;
        continue;
      case JsonStep.ObjCommaOrClose:
        if (c === 0x2c) {
          stack[stack.length - 1] = JsonStep.ObjKey;
          i += 1;
          continue;
        }
        if (c === 0x7d) {
          i += 1;
          if (closeFrame(i) === 0) return i;
          continue;
        }
        return -i;
      case JsonStep.ArrCommaOrClose:
        if (c === 0x2c) {
          stack[stack.length - 1] = JsonStep.ArrValue;
          i += 1;
          continue;
        }
        if (c === 0x5d) {
          i += 1;
          if (closeFrame(i) === 0) return i;
          continue;
        }
        return -i;
      case JsonStep.ArrFirstValOrClose:
      case JsonStep.ObjValue:
      case JsonStep.ArrValue: {
        if ((top === JsonStep.ArrFirstValOrClose || (top === JsonStep.ArrValue && pythonic)) && c === 0x5d) {
          i += 1;
          if (closeFrame(i) === 0) return i;
          continue;
        }
        const afterValue = top === JsonStep.ObjValue ? JsonStep.ObjCommaOrClose : JsonStep.ArrCommaOrClose;
        if (c === 0x7b || c === 0x5b) {
          stack[stack.length - 1] = afterValue;
          stack.push(c === 0x7b ? JsonStep.ObjFirstKeyOrClose : JsonStep.ArrFirstValOrClose);
          openOffsets.push(i);
          i += 1;
          continue;
        }
        const end = readScalarValue(text, i);
        if (end < 0) return end;
        if (c === 0x27 || c === 0x54 || c === 0x46 || c === 0x4e) pythonic = true;
        stack[stack.length - 1] = afterValue;
        i = end;
        continue;
      }
    }
  }
  return -i;
}

/** Grammatical JSON-like containers at line starts or after host markers and log prefixes. */
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
    const blankEnd = skipBlanks(line.text, 0);
    const logPrefixed = column > blankEnd && hostMarkerLength(line.text.slice(blankEnd)) === 0;
    let pos = line.from + column;
    let nextLine = lineNumber + 1;
    while (pos < line.to) {
      const ch = text.charCodeAt(pos);
      if (ch !== 0x7b && ch !== 0x5b) {
        pos += 1;
        continue;
      }
      const nested: Array<{ from: number; to: number }> = [];
      const end = readJsonContainer(text, pos, nested);
      if (end > 0) {
        spans.push({ from: pos, to: end, rule: DATA_LIKE_RULE });
        if (end > line.to) {
          nextLine = doc.lineAt(end).number + 1;
          break;
        }
        pos = end;
        continue;
      }
      if (-end >= text.length && !logPrefixed) return spans;
      const failLine = -end >= text.length ? doc.lines + 1 : doc.lineAt(-end).number;
      if (failLine > lineNumber) {
        for (const child of nested) {
          if (child.from <= line.to) continue;
          const childLine = doc.lineAt(child.from);
          const childCol = containerStartColumn(childLine.text);
          if (childCol !== null && child.from >= childLine.from + childCol) {
            spans.push({ from: child.from, to: child.to, rule: DATA_LIKE_RULE });
          }
        }
        nextLine = failLine;
        break;
      }
      pos = Math.max(pos + 1, -end);
    }
    lineNumber = nextLine;
  }
  return spans;
}
