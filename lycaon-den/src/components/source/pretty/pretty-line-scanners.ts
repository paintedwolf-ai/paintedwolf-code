import type { PrettyLayoutFamily } from "./pretty-classify.ts";
import type { PrettyBudgetTracker } from "./pretty-limits.ts";
import type {
  PrettyLineLayoutState,
  PrettyLineRelativeBoundary,
} from "./pretty-types.ts";

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
        }
        if (!pushBoundary(i, depth)) return "budget";
      }
      inComment = true;
      i += 4;
      continue;
    }

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
          if (!pushBoundary(i, depth)) return "budget";
        }
      } else if (curKind === "close") {
        depth = Math.max(0, depth - 1);
        if (!pushBoundary(i, depth)) return "budget";
      } else {
        if (!pushBoundary(i, depth)) return "budget";
      }
    } else if (curKind === "close" && prevTagKind === null && i === scanStartCol) {
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
        quoteCode = 0;
        lastSigCode = c;
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
      if (nextIdx === lineLen && hasFollowingNewline) continue;
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
      if (nextIdx === lineLen && hasFollowingNewline) continue;
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
      if (nextIdx === lineLen && hasFollowingNewline) continue;
      if (nextIdx < lineLen && lineText.charCodeAt(nextIdx) === 0x7d) continue;
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
