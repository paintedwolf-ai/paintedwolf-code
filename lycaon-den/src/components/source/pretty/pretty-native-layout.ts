import { syntaxTree } from "@codemirror/language";
import {
  type Annotation,
  type EditorState,
  type Extension,
  type Range,
  StateEffect,
  StateField,
  Text,
  Transaction,
} from "@codemirror/state";
import {
  Decoration,
  type DecorationSet,
  EditorView,
  WidgetType,
} from "@codemirror/view";
import {
  classifyDocumentFormat,
  type DocumentFormatMode,
  isClosingFence,
  isEnvelopeHeaderLine,
  isExcerptBarrierLine,
  matchEnvelopeOrLogPrefix,
  parseOpeningFence,
  type PrettyLayoutFamily,
  resolveFenceLayoutFamily,
} from "./pretty-classify.ts";
import { maxPrettyVisits, PrettyBudgetTracker } from "./pretty-limits.ts";
import type {
  PrettyLayoutBoundary,
  PrettyLineLayoutState,
  PrettyLineRelativeBoundary,
  PrettyNativeLayoutOptions,
  PrettyNativeLayoutResult,
  PrettyProjectionOutcome,
  PrettyResourceCounters,
  PrettyResourceLimits,
} from "./pretty-types.ts";

export class PrettyBreakWidget extends WidgetType {
  override toDOM(): HTMLElement {
    const el = document.createElement("div");
    el.className = "pw-virtual-break";
    el.setAttribute("aria-hidden", "true");
    return el;
  }

  override get estimatedHeight(): number {
    return 0;
  }

  override eq(other: WidgetType): boolean {
    return other instanceof PrettyBreakWidget;
  }
}

export class PrettyIndentWidget extends WidgetType {
  readonly depth: number;
  readonly indentWidthCh: number;

  constructor(depth: number, indentWidthCh = 2) {
    super();
    this.depth = depth;
    this.indentWidthCh = indentWidthCh;
  }

  override eq(other: WidgetType): boolean {
    return (
      other instanceof PrettyIndentWidget &&
      other.depth === this.depth &&
      other.indentWidthCh === this.indentWidthCh
    );
  }

  override toDOM(): HTMLElement {
    const el = document.createElement("span");
    el.className = "pw-virtual-indent";
    el.style.width = `${this.depth * this.indentWidthCh}ch`;
    el.setAttribute("aria-hidden", "true");
    return el;
  }
}

const SHARED_BREAK_WIDGET = new PrettyBreakWidget();
const SHARED_BREAK_DECORATION = Decoration.widget({
  widget: SHARED_BREAK_WIDGET,
  block: true,
  side: -1,
});

const INDENT_DECORATION_CACHE = new Map<string, Decoration>();

function getIndentDecoration(depth: number, indentWidthCh: number): Decoration {
  const key = `${depth}:${indentWidthCh}`;
  let cached = INDENT_DECORATION_CACHE.get(key);
  if (!cached) {
    cached = Decoration.widget({
      widget: new PrettyIndentWidget(depth, indentWidthCh),
      side: 2,
    });
    INDENT_DECORATION_CACHE.set(key, cached);
  }
  return cached;
}

const prettyNativeTheme = EditorView.baseTheme({
  ".pw-virtual-break": {
    height: "0",
    margin: "0",
    padding: "0",
    overflow: "hidden",
  },
  ".pw-virtual-indent": {
    display: "inline-block",
    height: "1em",
    pointerEvents: "none",
  },
});

function isHorizontalSpace(code: number): boolean {
  return code === 0x20 || code === 0x09;
}

function nextNonSpaceInLine(
  lineText: string,
  from: number,
  budget: PrettyBudgetTracker,
): number | "budget" {
  let idx = from;
  while (idx < lineText.length && isHorizontalSpace(lineText.charCodeAt(idx))) {
    if (!budget.recordVisit(1)) return "budget";
    idx += 1;
  }
  return idx;
}

function prevNonSpaceInLine(
  lineText: string,
  before: number,
  minCol: number,
  budget: PrettyBudgetTracker,
): number | "budget" {
  let idx = before - 1;
  while (idx >= minCol && isHorizontalSpace(lineText.charCodeAt(idx))) {
    if (!budget.recordVisit(1)) return "budget";
    idx -= 1;
  }
  return idx;
}

const HTML_VOID_TAGS = new Set([
  "area",
  "base",
  "br",
  "col",
  "embed",
  "hr",
  "img",
  "input",
  "link",
  "meta",
  "param",
  "source",
  "track",
  "wbr",
]);

const XML_WHITESPACE_SENSITIVE_TAGS = new Set([
  "pre",
  "code",
  "textarea",
  "script",
  "style",
]);

function isAsciiAlphaNumOrDash(code: number): boolean {
  return (
    (code >= 0x41 && code <= 0x5a) ||
    (code >= 0x61 && code <= 0x7a) ||
    (code >= 0x30 && code <= 0x39) ||
    code === 0x2d ||
    code === 0x5f ||
    code === 0x3a ||
    code === 0x2e
  );
}

function extractXmlTagName(lineText: string, nameStart: number, limit: number): string {
  let end = nameStart;
  while (end < limit && isAsciiAlphaNumOrDash(lineText.charCodeAt(end))) {
    end += 1;
  }
  let out = "";
  for (let k = nameStart; k < end; k++) {
    const c = lineText.charCodeAt(k);
    out += String.fromCharCode(c >= 0x41 && c <= 0x5a ? c + 32 : c);
  }
  return out;
}

/**
 * Single-pass O(lineLength) scanner for XML/HTML/SVG lines.
 * Breaks only between adjacent structural tags (`><`) while preserving literal/mixed
 * text (`<item>Alpha</item>`), comments (`<!-- ... -->`), CDATA (`<![CDATA[...]]>`),
 * and whitespace-sensitive `<pre>`, `<code>`, `<textarea>`, `<script>`, `<style>` blocks.
 */
function scanXmlHtmlLineBoundaries(
  lineText: string,
  lineNumber: number,
  lineFrom: number,
  startDepth: number,
  startInBlockComment: boolean,
  startXmlRawTag: string | null,
  scanStartCol: number,
  budget: PrettyBudgetTracker,
): PrettyLineLayoutState | "budget" {
  const lineTo = lineFrom + lineText.length;
  const lineLen = lineText.length;
  let depth = startDepth;
  let inComment = startInBlockComment;
  let rawTag = startXmlRawTag;
  const relBoundaries: PrettyLineRelativeBoundary[] = [];

  const pushBoundary = (relPos: number, bDepth: number): boolean => {
    const prev = relBoundaries[relBoundaries.length - 1];
    if (prev && prev.relPos === relPos) {
      return true;
    }
    if (!budget.recordOutputRun(bDepth * 2, bDepth * 2, 1)) {
      return false;
    }
    relBoundaries.push({ relPos, depth: bDepth });
    return true;
  };

  let i = scanStartCol;
  let prevTagKind: "open" | "close" | "self" | null = null;
  let prevTagName = "";
  let prevTagEnd = -1;

  while (i < lineLen) {
    if (!budget.recordVisit(1)) return "budget";

    if (inComment) {
      if (
        lineText.charCodeAt(i) === 0x2d &&
        i + 2 < lineLen &&
        lineText.charCodeAt(i + 1) === 0x2d &&
        lineText.charCodeAt(i + 2) === 0x3e
      ) {
        inComment = false;
        prevTagKind = "self";
        prevTagName = "!comment";
        prevTagEnd = i + 3;
        i += 3;
        continue;
      }
      i += 1;
      continue;
    }

    if (rawTag !== null) {
      if (
        lineText.charCodeAt(i) === 0x3c &&
        i + 2 < lineLen &&
        lineText.charCodeAt(i + 1) === 0x2f
      ) {
        const closeName = extractXmlTagName(lineText, i + 2, lineLen);
        if (closeName === rawTag) {
          let closeEnd = i + 2 + closeName.length;
          while (closeEnd < lineLen && lineText.charCodeAt(closeEnd) !== 0x3e) {
            if (!budget.recordVisit(1)) return "budget";
            closeEnd += 1;
          }
          if (closeEnd < lineLen) {
            rawTag = null;
            prevTagKind = "close";
            prevTagName = closeName;
            prevTagEnd = closeEnd + 1;
            i = closeEnd + 1;
            continue;
          }
        }
      }
      i += 1;
      continue;
    }

    const c = lineText.charCodeAt(i);
    if (c !== 0x3c) {
      i += 1;
      continue;
    }

    // Check if there was only horizontal whitespace between `prevTagEnd` and `i`.
    let adjacentToPrevTag = false;
    if (prevTagEnd >= scanStartCol) {
      adjacentToPrevTag = true;
      for (let k = prevTagEnd; k < i; k++) {
        if (!isHorizontalSpace(lineText.charCodeAt(k))) {
          adjacentToPrevTag = false;
          break;
        }
      }
    }

    // Comment `<!--`
    if (
      i + 3 < lineLen &&
      lineText.charCodeAt(i + 1) === 0x21 &&
      lineText.charCodeAt(i + 2) === 0x2d &&
      lineText.charCodeAt(i + 3) === 0x2d
    ) {
      if (!budget.recordToken()) return "budget";
      if (adjacentToPrevTag && prevTagKind !== null) {
        if (prevTagKind === "open") {
          depth += 1;
          if (!budget.recordDepth(depth)) return "budget";
        }
        if (!pushBoundary(i, depth)) return "budget";
      }
      inComment = true;
      i += 4;
      continue;
    }

    // CDATA `<![CDATA[`
    if (lineText.startsWith("<![CDATA[", i)) {
      if (!budget.recordToken()) return "budget";
      const endCdata = lineText.indexOf("]]>", i + 9);
      if (endCdata >= 0) {
        i = endCdata + 3;
        prevTagEnd = -1;
        prevTagKind = null;
        continue;
      }
      break;
    }

    // Scan to closing `>` respecting attribute quotes `"..."` and `'...'`.
    let quoteCode = 0;
    let tagEnd = i + 1;
    while (tagEnd < lineLen) {
      if (!budget.recordVisit(1)) return "budget";
      const tc = lineText.charCodeAt(tagEnd);
      if (quoteCode !== 0) {
        if (tc === quoteCode) quoteCode = 0;
      } else if (tc === 0x22 || tc === 0x27) {
        quoteCode = tc;
      } else if (tc === 0x3e) {
        break;
      }
      tagEnd += 1;
    }

    if (tagEnd >= lineLen) {
      break;
    }

    if (!budget.recordToken()) return "budget";
    const firstInside = i + 1 < tagEnd ? lineText.charCodeAt(i + 1) : 0;
    let curKind: "open" | "close" | "self" = "open";
    let curName = "";

    if (firstInside === 0x2f) {
      curKind = "close";
      curName = extractXmlTagName(lineText, i + 2, tagEnd);
    } else if (firstInside === 0x21 || firstInside === 0x3f) {
      curKind = "self";
      curName = "!decl";
    } else {
      curName = extractXmlTagName(lineText, i + 1, tagEnd);
      let beforeClose = tagEnd - 1;
      while (beforeClose > i && isHorizontalSpace(lineText.charCodeAt(beforeClose))) {
        beforeClose -= 1;
      }
      if (
        lineText.charCodeAt(beforeClose) === 0x2f ||
        HTML_VOID_TAGS.has(curName)
      ) {
        curKind = "self";
      }
    }

    if (adjacentToPrevTag && prevTagKind !== null) {
      if (prevTagKind === "open") {
        if (curKind === "close" && curName === prevTagName) {
          // Empty element `<tag></tag>` stays on the same line.
        } else if (curKind === "close") {
          if (!pushBoundary(i, depth)) return "budget";
        } else {
          depth += 1;
          if (!budget.recordDepth(depth)) return "budget";
          if (!pushBoundary(i, depth)) return "budget";
        }
      } else {
        // prevTagKind is "close" or "self"
        if (curKind === "close") {
          depth = Math.max(0, depth - 1);
          if (!pushBoundary(i, depth)) return "budget";
        } else {
          if (!pushBoundary(i, depth)) return "budget";
        }
      }
    } else if (curKind === "close" && prevTagKind === null && i === scanStartCol) {
      // Line begins with a closing tag: reduce running depth for subsequent lines.
      depth = Math.max(0, depth - 1);
    }

    if (curKind === "open" && XML_WHITESPACE_SENSITIVE_TAGS.has(curName)) {
      rawTag = curName;
    }

    prevTagKind = curKind;
    prevTagName = curName;
    prevTagEnd = tagEnd + 1;
    i = tagEnd + 1;
  }

  // If the line ended with an unclosed `<tag>` that was not followed by another tag on this line,
  // advance depth for subsequent lines unless it was a raw tag or closed on the same line.
  if (prevTagKind === "open" && rawTag === null) {
    let trailingSpaceOnly = true;
    for (let k = prevTagEnd; k < lineLen; k++) {
      if (!isHorizontalSpace(lineText.charCodeAt(k))) {
        trailingSpaceOnly = false;
        break;
      }
    }
    if (trailingSpaceOnly) {
      depth += 1;
      if (!budget.recordDepth(depth)) return "budget";
    }
  }

  return {
    lineNumber,
    lineFrom,
    lineTo,
    startDepth,
    endDepth: depth,
    startInBlockComment,
    endInBlockComment: inComment,
    startXmlRawTag,
    endXmlRawTag: rawTag,
    family: "xml-html",
    eligible: true,
    scanStartCol,
    relBoundaries,
  };
}

function canStartRegexAfterCode(code: number): boolean {
  return (
    code === 0 ||
    code === 0x28 || // (
    code === 0x2c || // ,
    code === 0x3d || // =
    code === 0x3a || // :
    code === 0x5b || // [
    code === 0x21 || // !
    code === 0x26 || // &
    code === 0x7c || // |
    code === 0x3f || // ?
    code === 0x7b || // {
    code === 0x7d || // }
    code === 0x3b || // ;
    code === 0x2b || // +
    code === 0x2d || // -
    code === 0x7e || // ~
    code === 0x2a || // *
    code === 0x25 || // %
    code === 0x3c || // <
    code === 0x3e || // >
    code === 0x5e // ^
  );
}

/**
 * Single-pass O(lineLength) lexical boundary scanner for one raw source line across
 * JSON, JSONL, JSONC/JSON5, XML/HTML, JavaScript/TypeScript, and CSS/SCSS/Less.
 * Never allocates substrings via `slice()` or executes regexes inside the character loop.
 * Preserves all source whitespace and tolerates malformed intermediate syntax.
 */
export function scanLineLexicalBoundaries(
  lineText: string,
  lineNumber: number,
  totalLines: number,
  lineFrom: number,
  startDepth: number,
  eligible: boolean,
  scanStartCol: number,
  budget: PrettyBudgetTracker,
  family: PrettyLayoutFamily = "json",
  startInBlockComment = false,
  startXmlRawTag: string | null = null,
): PrettyLineLayoutState | "budget" {
  const lineTo = lineFrom + lineText.length;
  if (!eligible || family === "passthrough" || scanStartCol >= lineText.length) {
    return {
      lineNumber,
      lineFrom,
      lineTo,
      startDepth,
      endDepth: startDepth,
      startInBlockComment,
      endInBlockComment: startInBlockComment,
      startXmlRawTag,
      endXmlRawTag: startXmlRawTag,
      family,
      eligible,
      scanStartCol,
      relBoundaries: [],
    };
  }

  if (family === "xml-html") {
    return scanXmlHtmlLineBoundaries(
      lineText,
      lineNumber,
      lineFrom,
      startDepth,
      startInBlockComment,
      startXmlRawTag,
      scanStartCol,
      budget,
    );
  }

  let depth = startDepth;
  let quoteCode = 0; // 0, 0x22 ("), 0x27 ('), 0x60 (`), or 0x2f (/) for regex
  let regexCharClass = false;
  let escape = false;
  let inBlockComment = startInBlockComment;
  let lastSigCode = 0;
  const delimStack: number[] = []; // tracks 0x7b ({), 0x5b ([), 0x28 (() on this line
  const relBoundaries: PrettyLineRelativeBoundary[] = [];
  const lineLen = lineText.length;
  const hasFollowingNewline = lineNumber < totalLines;
  const hasPrecedingNewline = lineNumber > 1 && scanStartCol === 0;

  const allowSingleQuote =
    family === "jsonc" || family === "javascript" || family === "css";
  const allowBacktick = family === "javascript";
  const allowComments =
    family === "jsonc" || family === "javascript" || family === "css";
  const allowSemicolon = family === "javascript" || family === "css";
  const allowComma = family !== "css";

  const pushBoundary = (relPos: number, bDepth: number): boolean => {
    const prev = relBoundaries[relBoundaries.length - 1];
    if (prev && prev.relPos === relPos) {
      return true;
    }
    if (!budget.recordOutputRun(bDepth * 2, bDepth * 2, 1)) {
      return false;
    }
    relBoundaries.push({ relPos, depth: bDepth });
    return true;
  };

  const isDirectlyInParen = (): boolean => {
    return delimStack.length > 0 && delimStack[delimStack.length - 1] === 0x28;
  };

  for (let i = scanStartCol; i < lineLen; i++) {
    if (!budget.recordVisit(1)) return "budget";
    const c = lineText.charCodeAt(i);

    if (inBlockComment) {
      if (c === 0x2a && i + 1 < lineLen && lineText.charCodeAt(i + 1) === 0x2f) {
        inBlockComment = false;
        i += 1;
      }
      continue;
    }

    if (quoteCode !== 0) {
      if (escape) {
        escape = false;
      } else if (c === 0x5c) {
        escape = true;
      } else if (quoteCode === 0x2f) {
        if (c === 0x5b) regexCharClass = true;
        else if (c === 0x5d) regexCharClass = false;
        else if (c === 0x2f && !regexCharClass) {
          quoteCode = 0;
          lastSigCode = 0x2f;
        }
      } else if (c === quoteCode) {
        quoteCode = 0;
        lastSigCode = c;
      }
      continue;
    }

    if (isHorizontalSpace(c)) {
      continue;
    }

    if (allowComments && c === 0x2f && i + 1 < lineLen) {
      const next = lineText.charCodeAt(i + 1);
      if (next === 0x2f && (family !== "css" || lastSigCode !== 0x3a)) {
        // Single-line `//` comment: ignore rest of line.
        break;
      }
      if (next === 0x2a) {
        inBlockComment = true;
        i += 1;
        continue;
      }
    }

    if (
      c === 0x22 ||
      (allowSingleQuote && c === 0x27) ||
      (allowBacktick && c === 0x60)
    ) {
      if (!budget.recordToken()) return "budget";
      quoteCode = c;
      continue;
    }

    if (family === "javascript" && c === 0x2f && canStartRegexAfterCode(lastSigCode)) {
      if (!budget.recordToken()) return "budget";
      quoteCode = 0x2f;
      regexCharClass = false;
      continue;
    }

    if (c === 0x28) {
      delimStack.push(0x28);
      lastSigCode = c;
      continue;
    }
    if (c === 0x29) {
      if (delimStack.length > 0 && delimStack[delimStack.length - 1] === 0x28) {
        delimStack.pop();
      }
      lastSigCode = c;
      continue;
    }

    if (c === 0x7b || (family !== "css" && c === 0x5b)) {
      if (!budget.recordToken()) return "budget";
      if (family === "css" && isDirectlyInParen()) {
        lastSigCode = c;
        continue;
      }
      delimStack.push(c);
      depth += 1;
      if (!budget.recordDepth(depth)) return "budget";
      lastSigCode = c;
      const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
      if (nextIdx === "budget") return "budget";
      if (nextIdx === lineLen && hasFollowingNewline) {
        continue;
      }
      if (nextIdx < lineLen) {
        const nextCode = lineText.charCodeAt(nextIdx);
        if (nextCode === 0x7d || (family !== "css" && nextCode === 0x5d)) {
          continue;
        }
      }
      if (!pushBoundary(i + 1, depth)) return "budget";
    } else if (c === 0x7d || (family !== "css" && c === 0x5d)) {
      if (!budget.recordToken()) return "budget";
      if (family === "css" && isDirectlyInParen()) {
        lastSigCode = c;
        continue;
      }
      const expectedOpen = c === 0x7d ? 0x7b : 0x5b;
      if (
        delimStack.length > 0 &&
        delimStack[delimStack.length - 1] === expectedOpen
      ) {
        delimStack.pop();
      }
      depth = Math.max(0, depth - 1);
      lastSigCode = c;
      const prevIdx = prevNonSpaceInLine(lineText, i, scanStartCol, budget);
      if (prevIdx === "budget") return "budget";
      let skipBeforeClose = false;
      if (prevIdx < scanStartCol) {
        if (hasPrecedingNewline) {
          skipBeforeClose = true;
        }
      } else {
        const prevCode = lineText.charCodeAt(prevIdx);
        if (prevCode === 0x7b || (family !== "css" && prevCode === 0x5b)) {
          skipBeforeClose = true;
        }
      }
      if (!skipBeforeClose) {
        if (!pushBoundary(i, depth)) return "budget";
      }
      // In CSS or JS/TS, if `}` is followed by another statement/rule on the same line
      // (and not followed by `,`, `;`, `}`, `]`, `)`), emit a break after `}` as well.
      if (c === 0x7d && (family === "css" || family === "javascript")) {
        const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
        if (nextIdx === "budget") return "budget";
        if (nextIdx < lineLen) {
          const nextCode = lineText.charCodeAt(nextIdx);
          if (
            nextCode !== 0x7d &&
            nextCode !== 0x5d &&
            nextCode !== 0x29 &&
            nextCode !== 0x2c &&
            nextCode !== 0x3b
          ) {
            if (!pushBoundary(i + 1, depth)) return "budget";
          }
        }
      }
    } else if (allowComma && c === 0x2c) {
      if (!budget.recordToken()) return "budget";
      lastSigCode = c;
      if (family === "javascript" && isDirectlyInParen()) {
        continue;
      }
      const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
      if (nextIdx === "budget") return "budget";
      if (nextIdx === lineLen && hasFollowingNewline) {
        continue;
      }
      // For trailing commas in JSONC or JS/TS before `}` or `]`, let the closing
      // delimiter's own pre-boundary handle the break at `depth - 1`.
      if (
        (family === "jsonc" || family === "javascript") &&
        nextIdx < lineLen
      ) {
        const nextCode = lineText.charCodeAt(nextIdx);
        if (nextCode === 0x7d || nextCode === 0x5d) {
          continue;
        }
      }
      if (!pushBoundary(i + 1, depth)) return "budget";
    } else if (allowSemicolon && c === 0x3b) {
      if (!budget.recordToken()) return "budget";
      lastSigCode = c;
      if (isDirectlyInParen()) {
        continue;
      }
      const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
      if (nextIdx === "budget") return "budget";
      if (nextIdx === lineLen && hasFollowingNewline) {
        continue;
      }
      if (nextIdx < lineLen && lineText.charCodeAt(nextIdx) === 0x7d) {
        continue;
      }
      if (!pushBoundary(i + 1, depth)) return "budget";
    } else {
      lastSigCode = c;
    }
  }

  return {
    lineNumber,
    lineFrom,
    lineTo,
    startDepth,
    endDepth: depth,
    startInBlockComment,
    endInBlockComment: inBlockComment,
    startXmlRawTag,
    endXmlRawTag: startXmlRawTag,
    family,
    eligible: true,
    scanStartCol,
    relBoundaries,
  };
}

function appendLineDecorations(
  target: Range<Decoration>[],
  lineState: PrettyLineLayoutState,
  indentWidthCh: number,
): void {
  const base = lineState.lineFrom;
  for (const b of lineState.relBoundaries) {
    const pos = base + b.relPos;
    target.push(SHARED_BREAK_DECORATION.range(pos));
    if (b.depth > 0) {
      target.push(getIndentDecoration(b.depth, indentWidthCh).range(pos));
    }
  }
}

function looksLikeStructuredJsonCandidate(text: string): boolean {
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i);
    if (c === 0x7b || c === 0x7d || c === 0x5b || c === 0x5d) return true;
  }
  return /"\s*:/.test(text);
}

function looksLikeStandaloneXmlCandidate(text: string): boolean {
  const trimmed = text.trim();
  return (
    trimmed.startsWith("<") &&
    trimmed.endsWith(">") &&
    /><[A-Za-z/?!]/.test(trimmed)
  );
}

interface LineEligibility {
  readonly eligible: boolean;
  readonly family: PrettyLayoutFamily;
  readonly scanStartCol: number;
  readonly resetDepth: boolean;
}

function determineDocumentLineEligibility(
  doc: Text,
  mode: DocumentFormatMode,
  rawText: string,
  budget: PrettyBudgetTracker,
): readonly LineEligibility[] | "passthrough" | "budget" {
  const totalLines = doc.lines;
  if (mode === "code-passthrough") return "passthrough";

  if (
    mode === "json" ||
    mode === "jsonc" ||
    mode === "xml-html" ||
    mode === "javascript" ||
    mode === "css"
  ) {
    const out: LineEligibility[] = new Array(totalLines);
    for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
      out[lineNum - 1] = {
        eligible: true,
        family: mode,
        scanStartCol: 0,
        resetDepth: lineNum === 1,
      };
    }
    return out;
  }

  if (mode === "jsonl") {
    const out: LineEligibility[] = new Array(totalLines);
    for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
      out[lineNum - 1] = {
        eligible: true,
        family: "jsonl",
        scanStartCol: 0,
        resetDepth: true,
      };
    }
    return out;
  }

  if (mode === "markdown" || /^(?: {0,3}(?:```|~~~))/m.test(rawText)) {
    const out: LineEligibility[] = new Array(totalLines);
    for (let i = 0; i < totalLines; i++) {
      out[i] = {
        eligible: false,
        family: "passthrough",
        scanStartCol: 0,
        resetDepth: true,
      };
    }
    let lineNum = 1;
    while (lineNum <= totalLines) {
      if (!budget.recordVisit(1)) return "budget";
      const lineText = doc.line(lineNum).text;
      const fence = parseOpeningFence(lineText);
      if (!fence) {
        lineNum += 1;
        continue;
      }
      let closeLineNum = -1;
      for (let j = lineNum + 1; j <= totalLines; j++) {
        if (!budget.recordVisit(1)) return "budget";
        if (isClosingFence(doc.line(j).text, fence.char, fence.len)) {
          closeLineNum = j;
          break;
        }
      }
      if (closeLineNum < 0) break;
      const fenceFamily = resolveFenceLayoutFamily(fence.lang);
      if (fenceFamily) {
        for (let bodyLine = lineNum + 1; bodyLine < closeLineNum; bodyLine++) {
          out[bodyLine - 1] = {
            eligible: true,
            family: fenceFamily,
            scanStartCol: 0,
            resetDepth: fenceFamily === "jsonl" || bodyLine === lineNum + 1,
          };
        }
      }
      lineNum = closeLineNum + 1;
    }
    return out;
  }

  // mode === "auto"
  if (looksLikeStandaloneXmlCandidate(rawText)) {
    const out: LineEligibility[] = new Array(totalLines);
    for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
      out[lineNum - 1] = {
        eligible: true,
        family: "xml-html",
        scanStartCol: 0,
        resetDepth: lineNum === 1,
      };
    }
    return out;
  }

  if (!looksLikeStructuredJsonCandidate(rawText)) {
    return "passthrough";
  }

  // Check if any line has an excerpt barrier (`...`, compaction banner, etc.) or envelope/log prefix.
  let hasBarrier = false;
  let hasEnvelope = false;
  for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
    const lineText = doc.line(lineNum).text;
    if (isExcerptBarrierLine(lineText)) hasBarrier = true;
    if (
      isEnvelopeHeaderLine(lineText) ||
      matchEnvelopeOrLogPrefix(lineText) !== null
    ) {
      hasEnvelope = true;
    }
  }

  if (!hasBarrier && !hasEnvelope) {
    let allLinesStartContainer = totalLines >= 2;
    for (
      let lineNum = 1;
      lineNum <= totalLines && allLinesStartContainer;
      lineNum++
    ) {
      const t = doc.line(lineNum).text.trim();
      if (t.length === 0) continue;
      if (!(t.startsWith("{") || t.startsWith("["))) {
        allLinesStartContainer = false;
      }
    }
    const family: PrettyLayoutFamily = allLinesStartContainer ? "jsonl" : "json";
    const out: LineEligibility[] = new Array(totalLines);
    for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
      out[lineNum - 1] = {
        eligible: true,
        family,
        scanStartCol: 0,
        resetDepth: allLinesStartContainer || lineNum === 1,
      };
    }
    return out;
  }

  // Envelope / log / barrier-framed document: only format complete JSON bodies and keep barriers/headers raw.
  const out: LineEligibility[] = new Array(totalLines);
  for (let i = 0; i < totalLines; i++) {
    out[i] = {
      eligible: false,
      family: "passthrough",
      scanStartCol: 0,
      resetDepth: true,
    };
  }

  for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
    if (!budget.recordVisit(1)) return "budget";
    const lineText = doc.line(lineNum).text;
    if (isExcerptBarrierLine(lineText)) continue;
    const prefixCol = matchEnvelopeOrLogPrefix(lineText);
    if (prefixCol !== null) {
      out[lineNum - 1] = {
        eligible: true,
        family: "json",
        scanStartCol: prefixCol,
        resetDepth: true,
      };
      continue;
    }
    if (isEnvelopeHeaderLine(lineText)) continue;
    const trimmed = lineText.trimStart();
    if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
      let depth = 0;
      let quote = false;
      let escape = false;
      let closedLine = -1;
      for (let k = lineNum; k <= totalLines; k++) {
        const kText = doc.line(k).text;
        if (k > lineNum && isExcerptBarrierLine(kText)) {
          closedLine = -1;
          break;
        }
        for (let p = 0; p < kText.length; p++) {
          if (!budget.recordVisit(1)) return "budget";
          const c = kText.charCodeAt(p);
          if (quote) {
            if (escape) escape = false;
            else if (c === 0x5c) escape = true;
            else if (c === 0x22) quote = false;
            continue;
          }
          if (c === 0x22) quote = true;
          else if (c === 0x7b || c === 0x5b) depth += 1;
          else if (c === 0x7d || c === 0x5d) {
            depth -= 1;
            if (depth === 0) {
              closedLine = k;
              break;
            }
          }
        }
        if (closedLine >= 0) break;
      }
      if (closedLine >= lineNum) {
        for (let k = lineNum; k <= closedLine; k++) {
          out[k - 1] = {
            eligible: true,
            family: "json",
            scanStartCol: 0,
            resetDepth: k === lineNum,
          };
        }
        lineNum = closedLine;
      }
    }
  }
  return out;
}

function modeToPrimaryFamily(mode: DocumentFormatMode): PrettyLayoutFamily {
  if (mode === "code-passthrough") return "passthrough";
  if (mode === "auto") return "mixed";
  return mode;
}

function accountSyntaxTreeNodesInRange(
  state: EditorState | undefined,
  from: number,
  to: number,
  budget: PrettyBudgetTracker,
): boolean {
  if (!state) return true;
  const tree = syntaxTree(state);
  if (tree.length === 0) return true;
  let withinBudget = true;
  tree.iterate({
    from,
    to,
    enter: () => {
      if (!budget.recordSyntaxNode(1)) {
        withinBudget = false;
        return false;
      }
      return undefined;
    },
  });
  return withinBudget;
}

export function computePrettyNativeLayout(
  docOrText: Text | string,
  options: PrettyNativeLayoutOptions = {},
  editorState?: EditorState,
): PrettyNativeLayoutResult {
  const sourceIdentity = options.sourceIdentity ?? options.path ?? "";
  const sourceRevision = options.sourceRevision ?? 1;
  const doc =
    typeof docOrText === "string"
      ? Text.of(docOrText.split("\n"))
      : docOrText;
  const rawText = typeof docOrText === "string" ? docOrText : doc.toString();
  const budget = new PrettyBudgetTracker(
    rawText,
    options.limits,
    options.signal,
    options.shouldCancel,
  );
  const mode = classifyDocumentFormat(
    rawText,
    options.path,
    options.languageHint,
  );
  const primaryFamily = modeToPrimaryFamily(mode);

  if (!budget.checkInput()) {
    return {
      sourceIdentity,
      sourceRevision,
      outcome: budget.canceled ? "canceled" : "limit-exceeded",
      mode,
      family: primaryFamily,
      ...(budget.limitReason ? { limitReason: budget.limitReason } : {}),
      boundaries: [],
      lineStates: [],
      counters: budget.counters,
    };
  }

  if (doc.length === 0 || mode === "code-passthrough") {
    return {
      sourceIdentity,
      sourceRevision,
      outcome: "passthrough",
      mode,
      family: "passthrough",
      boundaries: [],
      lineStates: [],
      counters: budget.counters,
    };
  }

  if (doc.lines > budget.limits.maxOutputLines) {
    budget.limitReason = "maxOutputLines";
    return {
      sourceIdentity,
      sourceRevision,
      outcome: "limit-exceeded",
      mode,
      family: primaryFamily,
      limitReason: "maxOutputLines",
      boundaries: [],
      lineStates: [],
      counters: budget.counters,
    };
  }
  budget.counters.outputLines = doc.lines;
  budget.counters.outputUtf16Units = doc.length;
  budget.counters.outputUtf8Bytes = budget.counters.inputUtf8Bytes;

  if (!accountSyntaxTreeNodesInRange(editorState, 0, doc.length, budget)) {
    return {
      sourceIdentity,
      sourceRevision,
      outcome: budget.canceled ? "canceled" : "limit-exceeded",
      mode,
      family: primaryFamily,
      ...(budget.limitReason ? { limitReason: budget.limitReason } : {}),
      boundaries: [],
      lineStates: [],
      counters: budget.counters,
    };
  }

  const eligibilities = determineDocumentLineEligibility(
    doc,
    mode,
    rawText,
    budget,
  );
  if (eligibilities === "passthrough") {
    return {
      sourceIdentity,
      sourceRevision,
      outcome: "passthrough",
      mode,
      family: "passthrough",
      boundaries: [],
      lineStates: [],
      counters: budget.counters,
    };
  }
  if (eligibilities === "budget") {
    return {
      sourceIdentity,
      sourceRevision,
      outcome: budget.canceled ? "canceled" : "limit-exceeded",
      mode,
      family: primaryFamily,
      ...(budget.limitReason ? { limitReason: budget.limitReason } : {}),
      boundaries: [],
      lineStates: [],
      counters: budget.counters,
    };
  }

  const totalLines = doc.lines;
  const lineStates: PrettyLineLayoutState[] = new Array(totalLines);
  const boundaries: PrettyLayoutBoundary[] = [];
  let runningDepth = 0;
  let runningBlockComment = false;
  let runningXmlRawTag: string | null = null;

  for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
    const line = doc.line(lineNum);
    const elig = eligibilities[lineNum - 1]!;
    const startDepth = elig.resetDepth ? 0 : runningDepth;
    const startInBlockComment = elig.resetDepth ? false : runningBlockComment;
    const startXmlRawTag = elig.resetDepth ? null : runningXmlRawTag;
    const scanned = scanLineLexicalBoundaries(
      line.text,
      lineNum,
      totalLines,
      line.from,
      startDepth,
      elig.eligible,
      elig.scanStartCol,
      budget,
      elig.family,
      startInBlockComment,
      startXmlRawTag,
    );
    if (scanned === "budget") {
      return {
        sourceIdentity,
        sourceRevision,
        outcome: budget.canceled ? "canceled" : "limit-exceeded",
        mode,
        family: primaryFamily,
        ...(budget.limitReason ? { limitReason: budget.limitReason } : {}),
        boundaries: [],
        lineStates: [],
        counters: budget.counters,
      };
    }
    lineStates[lineNum - 1] = scanned;
    runningDepth = scanned.endDepth;
    runningBlockComment = scanned.endInBlockComment;
    runningXmlRawTag = scanned.endXmlRawTag;
    for (const b of scanned.relBoundaries) {
      boundaries.push({ pos: scanned.lineFrom + b.relPos, depth: b.depth });
    }
  }

  return {
    sourceIdentity,
    sourceRevision,
    outcome: boundaries.length > 0 ? "formatted" : "passthrough",
    mode,
    family: primaryFamily,
    boundaries,
    lineStates,
    counters: budget.counters,
  };
}

export interface PrettyNativeExtensionConfig {
  readonly enabled: boolean;
  readonly path?: string;
  readonly languageHint?: string;
  readonly indentWidthCh?: number;
  readonly limits?: Partial<PrettyResourceLimits>;
}

export interface PrettyNativeFieldState {
  readonly enabled: boolean;
  readonly path?: string;
  readonly languageHint?: string;
  readonly indentWidthCh: number;
  readonly limits?: Partial<PrettyResourceLimits>;
  readonly outcome: PrettyProjectionOutcome;
  readonly mode: DocumentFormatMode;
  readonly family: PrettyLayoutFamily;
  readonly limitReason?: string;
  readonly lineStates: readonly PrettyLineLayoutState[];
  readonly decorations: DecorationSet;
  readonly counters: PrettyResourceCounters;
  readonly lastIncrementalLinesScanned: number;
}

export const setPrettyNativeConfigEffect =
  StateEffect.define<PrettyNativeExtensionConfig>();

function buildFieldStateFromScratch(
  doc: Text,
  config: PrettyNativeExtensionConfig,
  editorState?: EditorState,
): PrettyNativeFieldState {
  const indentWidthCh = config.indentWidthCh ?? 2;
  if (!config.enabled) {
    return {
      enabled: false,
      ...(config.path !== undefined ? { path: config.path } : {}),
      ...(config.languageHint !== undefined
        ? { languageHint: config.languageHint }
        : {}),
      indentWidthCh,
      ...(config.limits !== undefined ? { limits: config.limits } : {}),
      outcome: "passthrough",
      mode: "auto",
      family: "passthrough",
      lineStates: [],
      decorations: Decoration.none,
      counters: {
        inputUtf8Bytes: 0,
        inputUtf16Units: doc.length,
        outputUtf8Bytes: 0,
        outputUtf16Units: doc.length,
        outputLines: doc.lines,
        maxDepthReached: 0,
        tokens: 0,
        mappingRuns: 0,
        syntaxNodesVisited: 0,
        visits: 0,
      },
      lastIncrementalLinesScanned: 0,
    };
  }

  const res = computePrettyNativeLayout(
    doc,
    {
      ...(config.path !== undefined ? { path: config.path } : {}),
      ...(config.languageHint !== undefined
        ? { languageHint: config.languageHint }
        : {}),
      indentWidthCh,
      ...(config.limits !== undefined ? { limits: config.limits } : {}),
    },
    editorState,
  );

  if (res.outcome !== "formatted" || res.boundaries.length === 0) {
    return {
      enabled: true,
      ...(config.path !== undefined ? { path: config.path } : {}),
      ...(config.languageHint !== undefined
        ? { languageHint: config.languageHint }
        : {}),
      indentWidthCh,
      ...(config.limits !== undefined ? { limits: config.limits } : {}),
      outcome: res.outcome,
      mode: res.mode,
      family: res.family,
      ...(res.limitReason ? { limitReason: res.limitReason } : {}),
      lineStates: res.lineStates,
      decorations: Decoration.none,
      counters: res.counters,
      lastIncrementalLinesScanned: res.lineStates.length,
    };
  }

  const ranges: Range<Decoration>[] = [];
  for (const ls of res.lineStates) {
    appendLineDecorations(ranges, ls, indentWidthCh);
  }
  return {
    enabled: true,
    ...(config.path !== undefined ? { path: config.path } : {}),
    ...(config.languageHint !== undefined
      ? { languageHint: config.languageHint }
      : {}),
    indentWidthCh,
    ...(config.limits !== undefined ? { limits: config.limits } : {}),
    outcome: "formatted",
    mode: res.mode,
    family: res.family,
    lineStates: res.lineStates,
    decorations: Decoration.set(ranges, true),
    counters: res.counters,
    lastIncrementalLinesScanned: res.lineStates.length,
  };
}

const INCREMENTAL_CAPABLE_MODES = new Set<DocumentFormatMode>([
  "json",
  "jsonl",
  "jsonc",
  "xml-html",
  "javascript",
  "css",
]);

/**
 * Incrementally updates line layout states and CodeMirror decorations across
 * changed line ranges (`tr.changes.iterChangedRanges`), stopping as soon as
 * `(endDepth, endInBlockComment, endXmlRawTag)` converge with unchanged subsequent lines.
 */
export function updatePrettyNativeLayoutIncrementally(
  prev: PrettyNativeFieldState,
  tr: Transaction,
): PrettyNativeFieldState {
  const newDoc = tr.state.doc;
  const oldDoc = tr.startState.doc;
  const config: PrettyNativeExtensionConfig = {
    enabled: prev.enabled,
    ...(prev.path !== undefined ? { path: prev.path } : {}),
    ...(prev.languageHint !== undefined
      ? { languageHint: prev.languageHint }
      : {}),
    indentWidthCh: prev.indentWidthCh,
    ...(prev.limits !== undefined ? { limits: prev.limits } : {}),
  };

  const canIncremental =
    INCREMENTAL_CAPABLE_MODES.has(prev.mode) &&
    prev.lineStates.length === oldDoc.lines &&
    prev.outcome !== "limit-exceeded" &&
    prev.outcome !== "canceled";

  if (!canIncremental) {
    return buildFieldStateFromScratch(newDoc, config, tr.state);
  }

  const budget = new PrettyBudgetTracker("", {
    ...prev.limits,
    maxVisits: prev.limits?.maxVisits ?? maxPrettyVisits(newDoc.length),
  });
  if (
    newDoc.length > budget.limits.maxInputUtf16Units ||
    newDoc.lines > budget.limits.maxOutputLines
  ) {
    return buildFieldStateFromScratch(newDoc, config, tr.state);
  }

  let minFromA = oldDoc.length;
  let maxToA = 0;
  let minFromB = newDoc.length;
  let maxToB = 0;
  let hasChange = false;

  tr.changes.iterChangedRanges((fromA, toA, fromB, toB) => {
    hasChange = true;
    if (fromA < minFromA) minFromA = fromA;
    if (toA > maxToA) maxToA = toA;
    if (fromB < minFromB) minFromB = fromB;
    if (toB > maxToB) maxToB = toB;
  });

  if (!hasChange) return prev;

  const oldEndLine = oldDoc.lineAt(Math.min(oldDoc.length, maxToA)).number;
  const newStartLine = newDoc.lineAt(minFromB).number;
  const newEndLine = newDoc.lineAt(Math.min(newDoc.length, maxToB)).number;

  // Expand by 1 line on each side because adjacent-real-newline suppression at line edges can depend on neighbor line existence.
  const rescanStartLine = Math.max(1, newStartLine - 1);
  const rescanMinEndLine = Math.min(newDoc.lines, newEndLine + 1);
  const lineDelta = newEndLine - oldEndLine;
  const totalLines = newDoc.lines;
  const isJsonl = prev.mode === "jsonl";
  const lineFamily = prev.mode as PrettyLayoutFamily;

  const nextLineStates: PrettyLineLayoutState[] = new Array(totalLines);
  for (let l = 1; l < rescanStartLine; l++) {
    nextLineStates[l - 1] = prev.lineStates[l - 1]!;
  }

  let runningDepth =
    rescanStartLine === 1 || isJsonl
      ? 0
      : prev.lineStates[rescanStartLine - 2]!.endDepth;
  let runningBlockComment =
    rescanStartLine === 1 || isJsonl
      ? false
      : prev.lineStates[rescanStartLine - 2]!.endInBlockComment;
  let runningXmlRawTag =
    rescanStartLine === 1 || isJsonl
      ? null
      : prev.lineStates[rescanStartLine - 2]!.endXmlRawTag;

  let actualRescanEndLine = rescanMinEndLine;
  let linesScanned = 0;
  const addedDecorations: Range<Decoration>[] = [];

  for (let l = rescanStartLine; l <= totalLines; l++) {
    const startDepth = isJsonl || l === 1 ? 0 : runningDepth;
    const startInBlockComment = isJsonl || l === 1 ? false : runningBlockComment;
    const startXmlRawTag = isJsonl || l === 1 ? null : runningXmlRawTag;

    if (l > rescanMinEndLine) {
      const oldLineNum = l - lineDelta;
      const oldState = prev.lineStates[oldLineNum - 1];
      const oldWasLast = oldLineNum === oldDoc.lines;
      const newIsLast = l === totalLines;
      if (
        oldState &&
        oldState.startDepth === startDepth &&
        oldState.startInBlockComment === startInBlockComment &&
        oldState.startXmlRawTag === startXmlRawTag &&
        oldWasLast === newIsLast
      ) {
        // Convergence achieved: copy remaining line states with shifted offsets.
        for (let rem = l; rem <= totalLines; rem++) {
          const remOld = prev.lineStates[rem - lineDelta - 1]!;
          const remLine = newDoc.line(rem);
          nextLineStates[rem - 1] = {
            lineNumber: rem,
            lineFrom: remLine.from,
            lineTo: remLine.to,
            startDepth: remOld.startDepth,
            endDepth: remOld.endDepth,
            startInBlockComment: remOld.startInBlockComment,
            endInBlockComment: remOld.endInBlockComment,
            startXmlRawTag: remOld.startXmlRawTag,
            endXmlRawTag: remOld.endXmlRawTag,
            family: remOld.family,
            eligible: remOld.eligible,
            scanStartCol: remOld.scanStartCol,
            relBoundaries: remOld.relBoundaries,
          };
        }
        actualRescanEndLine = l - 1;
        break;
      }
    }

    const line = newDoc.line(l);
    const scanned = scanLineLexicalBoundaries(
      line.text,
      l,
      totalLines,
      line.from,
      startDepth,
      true,
      0,
      budget,
      lineFamily,
      startInBlockComment,
      startXmlRawTag,
    );
    if (scanned === "budget") {
      return buildFieldStateFromScratch(newDoc, config, tr.state);
    }
    linesScanned += 1;
    actualRescanEndLine = l;
    nextLineStates[l - 1] = scanned;
    runningDepth = scanned.endDepth;
    runningBlockComment = scanned.endInBlockComment;
    runningXmlRawTag = scanned.endXmlRawTag;
    appendLineDecorations(addedDecorations, scanned, prev.indentWidthCh);
  }

  const filterFrom = newDoc.line(rescanStartLine).from;
  const filterTo = newDoc.line(actualRescanEndLine).to;
  accountSyntaxTreeNodesInRange(tr.state, filterFrom, filterTo, budget);

  const mappedDecorations = prev.decorations.map(tr.changes);
  const nextDecorations = mappedDecorations.update({
    filterFrom,
    filterTo,
    filter: () => false,
    add: addedDecorations,
    sort: true,
  });

  const hasAnyDecorations = nextDecorations.size > 0;
  return {
    ...prev,
    outcome: hasAnyDecorations ? "formatted" : "passthrough",
    lineStates: nextLineStates,
    decorations: nextDecorations,
    counters: {
      ...prev.counters,
      inputUtf16Units: newDoc.length,
      outputUtf16Units: newDoc.length,
      outputLines: newDoc.lines,
      maxDepthReached: Math.max(
        prev.counters.maxDepthReached,
        budget.counters.maxDepthReached,
      ),
      tokens: budget.counters.tokens,
      mappingRuns: budget.counters.mappingRuns,
      syntaxNodesVisited: budget.counters.syntaxNodesVisited,
      visits: budget.counters.visits,
    },
    lastIncrementalLinesScanned: linesScanned,
  };
}

export const prettyNativeStateField = StateField.define<PrettyNativeFieldState>({
  create(state) {
    return buildFieldStateFromScratch(state.doc, { enabled: false }, state);
  },
  update(prev, tr) {
    let nextConfig: PrettyNativeExtensionConfig | null = null;
    for (const effect of tr.effects) {
      if (effect.is(setPrettyNativeConfigEffect)) {
        nextConfig = effect.value;
      }
    }
    if (nextConfig !== null) {
      const sameConfig =
        prev.enabled === nextConfig.enabled &&
        prev.path === nextConfig.path &&
        prev.languageHint === nextConfig.languageHint &&
        prev.indentWidthCh === (nextConfig.indentWidthCh ?? 2) &&
        prev.limits === nextConfig.limits;
      if (sameConfig && !tr.docChanged) {
        return prev;
      }
      return buildFieldStateFromScratch(tr.state.doc, nextConfig, tr.state);
    }
    if (!prev.enabled) {
      return prev;
    }
    if (tr.docChanged) {
      return updatePrettyNativeLayoutIncrementally(prev, tr);
    }
    return prev;
  },
  provide: (field) =>
    EditorView.decorations.from(field, (state) => state.decorations),
});

export function createPrettyNativeDecorationsExtension(
  initialConfig: PrettyNativeExtensionConfig = { enabled: false },
): Extension {
  return [
    prettyNativeTheme,
    prettyNativeStateField.init((state) =>
      buildFieldStateFromScratch(state.doc, initialConfig, state),
    ),
  ];
}

export function applyPrettyNativeConfig(
  view: EditorView,
  config: PrettyNativeExtensionConfig,
  annotations?: readonly Annotation<unknown>[],
): void {
  const current = view.state.field(prettyNativeStateField, false);
  if (
    current &&
    current.enabled === config.enabled &&
    current.path === config.path &&
    current.languageHint === config.languageHint &&
    current.indentWidthCh === (config.indentWidthCh ?? 2)
  ) {
    return;
  }
  view.dispatch({
    effects: setPrettyNativeConfigEffect.of(config),
    ...(annotations ? { annotations: [...annotations] } : {}),
  });
}
