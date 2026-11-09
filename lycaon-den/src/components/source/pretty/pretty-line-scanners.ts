import type { PrettyLayoutFamily } from "./pretty-classify.ts";
import { type PrettyBudgetTracker, utf8ByteLength } from "./pretty-limits.ts";
import type {
  PrettyLineContinuationState,
  PrettyLineLayoutState,
  PrettyLineRelativeBoundary,
  PrettyUtf16Range,
} from "./pretty-types.ts";

export const INITIAL_LINE_CONTINUATION: PrettyLineContinuationState = {
  depth: 0,
  inBlockComment: false,
  inXmlCdata: false,
  xmlRawTag: null,
  quoteCode: 0,
  lastSigCode: 0,
  delimSignature: "",
};

export function continuationFromLineEnd(
  state: PrettyLineLayoutState,
): PrettyLineContinuationState {
  return {
    depth: state.endDepth,
    inBlockComment: state.endInBlockComment,
    inXmlCdata: state.endInXmlCdata,
    xmlRawTag: state.endXmlRawTag,
    quoteCode: state.endQuoteCode,
    lastSigCode: state.endLastSigCode,
    delimSignature: state.endDelimSignature,
  };
}

export function continuationEqualsLineStart(
  cont: PrettyLineContinuationState,
  lineState: PrettyLineLayoutState,
): boolean {
  return (
    lineState.startDepth === cont.depth &&
    lineState.startInBlockComment === cont.inBlockComment &&
    lineState.startInXmlCdata === cont.inXmlCdata &&
    lineState.startXmlRawTag === cont.xmlRawTag &&
    lineState.startQuoteCode === cont.quoteCode &&
    lineState.startLastSigCode === cont.lastSigCode &&
    lineState.startDelimSignature === cont.delimSignature
  );
}

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
  "area", "base", "br", "col", "embed", "hr", "img",
  "input", "link", "meta", "param", "source", "track", "wbr",
]);

const XML_WHITESPACE_SENSITIVE_TAGS = new Set([
  "pre", "code", "textarea", "script", "style",
]);

const JS_REGEX_PREFIX_KEYWORDS = new Set([
  "return", "case", "throw", "delete", "void", "typeof",
  "instanceof", "in", "of", "yield", "await", "else", "do",
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

function isJsIdentifierChar(code: number): boolean {
  return (
    (code >= 0x41 && code <= 0x5a) ||
    (code >= 0x61 && code <= 0x7a) ||
    (code >= 0x30 && code <= 0x39) ||
    code === 0x5f ||
    code === 0x24
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

function createProtectedSpanChecker(
  protectedSpans?: readonly PrettyUtf16Range[],
): (relPos: number) => boolean {
  if (!protectedSpans || protectedSpans.length === 0) {
    return () => false;
  }
  let cursor = 0;
  return (relPos: number): boolean => {
    while (
      cursor < protectedSpans.length &&
      protectedSpans[cursor]!.to <= relPos
    ) {
      cursor += 1;
    }
    const span = protectedSpans[cursor];
    return span !== undefined && span.from <= relPos && relPos < span.to;
  };
}

/**
 * Single-pass O(lineLength) scanner for XML/HTML/SVG lines.
 * Breaks only between adjacent structural tags (`><`) while preserving literal/mixed
 * text (`<item>Alpha</item>`), comments (`<!-- ... -->`), CDATA (`<![CDATA[...]]>`),
 * and whitespace-sensitive `<pre>`, `<code>`, `<textarea>`, `<script>`, `<style>` blocks
 * across line boundaries.
 */
function scanXmlHtmlLineBoundaries(
  lineText: string,
  cont: PrettyLineContinuationState,
  scanStartCol: number,
  budget: PrettyBudgetTracker,
  protectedSpans?: readonly PrettyUtf16Range[],
  lineUtf8BytesOverride?: number,
  lineSyntaxNodes = 0,
  lineSyntaxVisits = 0,
): PrettyLineLayoutState | "budget" {
  const lineLen = lineText.length;
  let depth = cont.depth;
  let inComment = cont.inBlockComment;
  let inCdata = cont.inXmlCdata;
  let rawTag = cont.xmlRawTag;
  let lineMaxDepth = depth;
  const relBoundaries: PrettyLineRelativeBoundary[] = [];
  const isSyntaxProtected = createProtectedSpanChecker(protectedSpans);

  const visitsBefore = budget.counters.visits;
  const tokensBefore = budget.counters.tokens;
  const runsBefore = budget.counters.mappingRuns;
  const outUtf16Before = budget.counters.outputUtf16Units;
  const outUtf8Before = budget.counters.outputUtf8Bytes;
  const outLinesBefore = budget.counters.outputLines;

  const pushBoundary = (relPos: number, bDepth: number): boolean => {
    const prev = relBoundaries[relBoundaries.length - 1];
    if (prev && prev.relPos === relPos) return true;
    if (!budget.recordOutputRun(bDepth * 2, bDepth * 2, 1)) return false;
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

    if (inCdata) {
      if (
        lineText.charCodeAt(i) === 0x5d &&
        i + 2 < lineLen &&
        lineText.charCodeAt(i + 1) === 0x5d &&
        lineText.charCodeAt(i + 2) === 0x3e
      ) {
        inCdata = false;
        prevTagKind = null;
        prevTagEnd = -1;
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

    if (isSyntaxProtected(i)) {
      i += 1;
      continue;
    }

    const c = lineText.charCodeAt(i);
    if (c !== 0x3c) {
      i += 1;
      continue;
    }

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
          if (depth > lineMaxDepth) lineMaxDepth = depth;
        }
        if (!pushBoundary(i, depth)) return "budget";
      }
      inComment = true;
      prevTagKind = null;
      prevTagEnd = -1;
      i += 4;
      continue;
    }

    if (lineText.startsWith("<![CDATA[", i)) {
      if (!budget.recordToken()) return "budget";
      inCdata = true;
      prevTagKind = null;
      prevTagEnd = -1;
      i += 9;
      continue;
    }

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

    if (tagEnd >= lineLen) break;
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
      if (lineText.charCodeAt(beforeClose) === 0x2f || HTML_VOID_TAGS.has(curName)) {
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
          if (depth > lineMaxDepth) lineMaxDepth = depth;
          if (!pushBoundary(i, depth)) return "budget";
        }
      } else if (curKind === "close") {
        depth = Math.max(0, depth - 1);
        if (!pushBoundary(i, depth)) return "budget";
      } else {
        if (!pushBoundary(i, depth)) return "budget";
      }
    } else if (curKind === "close" && prevTagKind === null) {
      let onlyLeadingSpaces = true;
      for (let k = scanStartCol; k < i; k++) {
        if (!isHorizontalSpace(lineText.charCodeAt(k))) {
          onlyLeadingSpaces = false;
          break;
        }
      }
      if (onlyLeadingSpaces) {
        depth = Math.max(0, depth - 1);
      }
    }

    if (curKind === "open" && XML_WHITESPACE_SENSITIVE_TAGS.has(curName)) {
      rawTag = curName;
    }

    prevTagKind = curKind;
    prevTagName = curName;
    prevTagEnd = tagEnd + 1;
    i = tagEnd + 1;
  }

  if (prevTagKind === "open" && rawTag === null && !inComment && !inCdata) {
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
      if (depth > lineMaxDepth) lineMaxDepth = depth;
    }
  }

  return {
    startDepth: cont.depth,
    endDepth: depth,
    startInBlockComment: cont.inBlockComment,
    endInBlockComment: inComment,
    startInXmlCdata: cont.inXmlCdata,
    endInXmlCdata: inCdata,
    startXmlRawTag: cont.xmlRawTag,
    endXmlRawTag: rawTag,
    startQuoteCode: 0,
    endQuoteCode: 0,
    startLastSigCode: 0,
    endLastSigCode: 0,
    startDelimSignature: "",
    endDelimSignature: "",
    family: "xml-html",
    eligible: true,
    scanStartCol,
    relBoundaries,
    metrics: {
      lineUtf8Bytes: lineUtf8BytesOverride ?? utf8ByteLength(lineText),
      outputUtf16Delta: budget.counters.outputUtf16Units - outUtf16Before,
      outputUtf8Delta: budget.counters.outputUtf8Bytes - outUtf8Before,
      outputLinesDelta: budget.counters.outputLines - outLinesBefore,
      mappingRuns: budget.counters.mappingRuns - runsBefore,
      tokens: budget.counters.tokens - tokensBefore,
      syntaxNodes: lineSyntaxNodes,
      visits: lineSyntaxVisits + (budget.counters.visits - visitsBefore),
      maxDepth: lineMaxDepth,
    },
  };
}

function canStartRegexAfterCode(code: number): boolean {
  return (
    code === 0 ||
    code === 0x28 ||
    code === 0x2c ||
    code === 0x3d ||
    code === 0x3a ||
    code === 0x5b ||
    code === 0x21 ||
    code === 0x26 ||
    code === 0x7c ||
    code === 0x3f ||
    code === 0x7b ||
    code === 0x7d ||
    code === 0x3b ||
    code === 0x2b ||
    code === 0x2d ||
    code === 0x7e ||
    code === 0x2a ||
    code === 0x25 ||
    code === 0x3c ||
    code === 0x3e ||
    code === 0x5e
  );
}

function canStartRegexAt(
  lineText: string,
  slashIdx: number,
  scanStartCol: number,
  lastSigCode: number,
): boolean {
  if (canStartRegexAfterCode(lastSigCode)) return true;
  if (lastSigCode < 0x61 || lastSigCode > 0x7a) return false;
  let end = slashIdx - 1;
  while (end >= scanStartCol && isHorizontalSpace(lineText.charCodeAt(end))) {
    end -= 1;
  }
  if (end < scanStartCol) return false;
  let start = end;
  while (start > scanStartCol && isJsIdentifierChar(lineText.charCodeAt(start - 1))) {
    start -= 1;
  }
  const wordLen = end - start + 1;
  if (wordLen < 2 || wordLen > 10) return false;
  return JS_REGEX_PREFIX_KEYWORDS.has(lineText.slice(start, end + 1));
}

function toContinuationState(
  startDepthOrCont: number | PrettyLineContinuationState,
  startInBlockComment: boolean,
  startXmlRawTag: string | null,
): PrettyLineContinuationState {
  if (typeof startDepthOrCont === "number") {
    return {
      depth: startDepthOrCont,
      inBlockComment: startInBlockComment,
      inXmlCdata: false,
      xmlRawTag: startXmlRawTag,
      quoteCode: 0,
      lastSigCode: 0,
      delimSignature: "",
    };
  }
  return startDepthOrCont;
}

/**
 * Single-pass O(lineLength) boundary scanner for one raw source line across
 * JSON, JSONL, JSONC/JSON5, XML/HTML, JavaScript/TypeScript, and CSS/SCSS/Less.
 * Uses CodeMirror `syntaxTree` literal/comment/raw spans when available and preserves
 * full bounded continuation state across lines for multiline templates, CDATA, comments,
 * and parenthesized calls.
 */
export function scanLineLexicalBoundaries(
  lineText: string,
  lineNumber: number,
  _totalLines: number,
  _lineFrom: number,
  startDepthOrCont: number | PrettyLineContinuationState,
  eligible: boolean,
  scanStartCol: number,
  budget: PrettyBudgetTracker,
  family: PrettyLayoutFamily = "json",
  startInBlockComment = false,
  startXmlRawTag: string | null = null,
  protectedSpans?: readonly PrettyUtf16Range[],
  lineUtf8BytesOverride?: number,
  lineSyntaxNodes = 0,
  lineSyntaxVisits = 0,
): PrettyLineLayoutState | "budget" {
  const cont = toContinuationState(
    startDepthOrCont,
    startInBlockComment,
    startXmlRawTag,
  );
  const computedLineUtf8 = lineUtf8BytesOverride ?? utf8ByteLength(lineText);

  if (!eligible || family === "passthrough" || scanStartCol >= lineText.length) {
    return {
      startDepth: cont.depth,
      endDepth: cont.depth,
      startInBlockComment: cont.inBlockComment,
      endInBlockComment: cont.inBlockComment,
      startInXmlCdata: cont.inXmlCdata,
      endInXmlCdata: cont.inXmlCdata,
      startXmlRawTag: cont.xmlRawTag,
      endXmlRawTag: cont.xmlRawTag,
      startQuoteCode: cont.quoteCode,
      endQuoteCode: cont.quoteCode,
      startLastSigCode: cont.lastSigCode,
      endLastSigCode: cont.lastSigCode,
      startDelimSignature: cont.delimSignature,
      endDelimSignature: cont.delimSignature,
      family,
      eligible,
      scanStartCol,
      relBoundaries: [],
      metrics: {
        lineUtf8Bytes: computedLineUtf8,
        outputUtf16Delta: 0,
        outputUtf8Delta: 0,
        outputLinesDelta: 0,
        mappingRuns: 0,
        tokens: 0,
        syntaxNodes: lineSyntaxNodes,
        visits: lineSyntaxVisits,
        maxDepth: cont.depth,
      },
    };
  }

  if (family === "xml-html") {
    return scanXmlHtmlLineBoundaries(
      lineText,
      cont,
      scanStartCol,
      budget,
      protectedSpans,
      computedLineUtf8,
      lineSyntaxNodes,
      lineSyntaxVisits,
    );
  }

  const visitsBefore = budget.counters.visits;
  const tokensBefore = budget.counters.tokens;
  const runsBefore = budget.counters.mappingRuns;
  const outUtf16Before = budget.counters.outputUtf16Units;
  const outUtf8Before = budget.counters.outputUtf8Bytes;
  const outLinesBefore = budget.counters.outputLines;

  let depth = cont.depth;
  let lineMaxDepth = depth;
  let quoteCode = cont.quoteCode; // 0, 0x22 ("), 0x27 ('), 0x60 (`), or 0x2f (/)
  let regexCharClass = false;
  let escape = false;
  let inBlockComment = cont.inBlockComment;
  let lastSigCode = cont.lastSigCode;
  const delimStack: number[] = [];
  for (let k = 0; k < cont.delimSignature.length; k++) {
    delimStack.push(cont.delimSignature.charCodeAt(k));
  }
  const relBoundaries: PrettyLineRelativeBoundary[] = [];
  const lineLen = lineText.length;
  const hasPrecedingNewline = lineNumber > 1 && scanStartCol === 0;
  const isSyntaxProtected = createProtectedSpanChecker(protectedSpans);

  const allowSingleQuote =
    family === "jsonc" || family === "javascript" || family === "css";
  const allowBacktick = family === "javascript";
  const allowComments =
    family === "jsonc" || family === "javascript" || family === "css";
  const allowSemicolon = family === "javascript" || family === "css";
  const allowComma = family !== "css";

  const pushBoundary = (relPos: number, bDepth: number): boolean => {
    const prev = relBoundaries[relBoundaries.length - 1];
    if (prev && prev.relPos === relPos) return true;
    if (!budget.recordOutputRun(bDepth * 2, bDepth * 2, 1)) return false;
    relBoundaries.push({ relPos, depth: bDepth });
    return true;
  };

  const isDirectlyInParen = (): boolean =>
    delimStack.length > 0 && delimStack[delimStack.length - 1] === 0x28;

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
        if (quoteCode === 0x60 && isSyntaxProtected(i)) {
          // Inside an outer Lezer TemplateString interior (e.g. nested `${`...`}`).
        } else {
          quoteCode = 0;
          lastSigCode = c;
        }
      }
      continue;
    }

    if (isHorizontalSpace(c)) continue;

    if (allowComments && c === 0x2f && i + 1 < lineLen) {
      const next = lineText.charCodeAt(i + 1);
      if (next === 0x2f && (family !== "css" || lastSigCode !== 0x3a)) {
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

    if (isSyntaxProtected(i)) {
      lastSigCode = c;
      continue;
    }

    if (
      family === "javascript" &&
      c === 0x2f &&
      canStartRegexAt(lineText, i, scanStartCol, lastSigCode)
    ) {
      if (!budget.recordToken()) return "budget";
      quoteCode = 0x2f;
      regexCharClass = false;
      continue;
    }

    if (c === 0x28) {
      if (!budget.recordDepth(delimStack.length + 1)) return "budget";
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
      if (!budget.recordDepth(Math.max(depth, delimStack.length))) return "budget";
      if (depth > lineMaxDepth) lineMaxDepth = depth;
      lastSigCode = c;
      const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
      if (nextIdx === "budget") return "budget";
      if (nextIdx === lineLen) continue;
      if (nextIdx < lineLen) {
        const nextCode = lineText.charCodeAt(nextIdx);
        if (nextCode === 0x7d || (family !== "css" && nextCode === 0x5d)) continue;
      }
      if (!pushBoundary(i + 1, depth)) return "budget";
    } else if (c === 0x7d || (family !== "css" && c === 0x5d)) {
      if (!budget.recordToken()) return "budget";
      if (family === "css" && isDirectlyInParen()) {
        lastSigCode = c;
        continue;
      }
      const expectedOpen = c === 0x7d ? 0x7b : 0x5b;
      if (delimStack.length > 0 && delimStack[delimStack.length - 1] === expectedOpen) {
        delimStack.pop();
      }
      depth = Math.max(0, depth - 1);
      lastSigCode = c;
      const prevIdx = prevNonSpaceInLine(lineText, i, scanStartCol, budget);
      if (prevIdx === "budget") return "budget";
      let skipBeforeClose = false;
      if (prevIdx < scanStartCol) {
        if (hasPrecedingNewline) skipBeforeClose = true;
      } else {
        const prevCode = lineText.charCodeAt(prevIdx);
        if (prevCode === 0x7b || (family !== "css" && prevCode === 0x5b)) {
          skipBeforeClose = true;
        }
      }
      if (!skipBeforeClose) {
        if (!pushBoundary(i, depth)) return "budget";
      }
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
      if (family === "javascript" && isDirectlyInParen()) continue;
      const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
      if (nextIdx === "budget") return "budget";
      if (nextIdx === lineLen) continue;
      if ((family === "jsonc" || family === "javascript") && nextIdx < lineLen) {
        const nextCode = lineText.charCodeAt(nextIdx);
        if (nextCode === 0x7d || nextCode === 0x5d) continue;
      }
      if (!pushBoundary(i + 1, depth)) return "budget";
    } else if (allowSemicolon && c === 0x3b) {
      if (!budget.recordToken()) return "budget";
      lastSigCode = c;
      if (isDirectlyInParen()) continue;
      const nextIdx = nextNonSpaceInLine(lineText, i + 1, budget);
      if (nextIdx === "budget") return "budget";
      if (nextIdx === lineLen) continue;
      if (nextIdx < lineLen && lineText.charCodeAt(nextIdx) === 0x7d) continue;
      if (!pushBoundary(i + 1, depth)) return "budget";
    } else {
      lastSigCode = c;
    }
  }

  const endQuoteCode =
    quoteCode === 0x60 || (escape && (quoteCode === 0x22 || quoteCode === 0x27))
      ? quoteCode
      : 0;
  const endDelimSignature =
    delimStack.length === 0 ? "" : String.fromCharCode(...delimStack);

  return {
    startDepth: cont.depth,
    endDepth: depth,
    startInBlockComment: cont.inBlockComment,
    endInBlockComment: inBlockComment,
    startInXmlCdata: cont.inXmlCdata,
    endInXmlCdata: false,
    startXmlRawTag: cont.xmlRawTag,
    endXmlRawTag: cont.xmlRawTag,
    startQuoteCode: cont.quoteCode,
    endQuoteCode,
    startLastSigCode: cont.lastSigCode,
    endLastSigCode: lastSigCode,
    startDelimSignature: cont.delimSignature,
    endDelimSignature,
    family,
    eligible: true,
    scanStartCol,
    relBoundaries,
    metrics: {
      lineUtf8Bytes: computedLineUtf8,
      outputUtf16Delta: budget.counters.outputUtf16Units - outUtf16Before,
      outputUtf8Delta: budget.counters.outputUtf8Bytes - outUtf8Before,
      outputLinesDelta: budget.counters.outputLines - outLinesBefore,
      mappingRuns: budget.counters.mappingRuns - runsBefore,
      tokens: budget.counters.tokens - tokensBefore,
      syntaxNodes: lineSyntaxNodes,
      visits: lineSyntaxVisits + (budget.counters.visits - visitsBefore),
      maxDepth: lineMaxDepth,
    },
  };
}
