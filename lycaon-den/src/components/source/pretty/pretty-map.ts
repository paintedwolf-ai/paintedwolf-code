import type {
  PrettyDisplayLineInfo, PrettyInputRuneSpan, PrettyLineCol, PrettyOriginRun,
  PrettyProjectedRuneSpan, PrettyRuneOffsetTable, PrettySourceMap, PrettyUtf16Range,
} from "./pretty-types.ts";

function buildLineStarts(text: string): Int32Array {
  let count = 1;
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 0x0a) count += 1;
  const starts = new Int32Array(count);
  let idx = 1;
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 0x0a) starts[idx++] = i + 1;
  return starts;
}

function findLineNumber(lineStarts: Int32Array, offset: number): number {
  let lo = 0, hi = lineStarts.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1;
    if ((lineStarts[mid] ?? 0) <= offset) lo = mid + 1;
    else hi = mid - 1;
  }
  return Math.max(1, hi + 1);
}

/** O(log N) conversion from display UTF-16 offset to raw source UTF-16 offset. */
export function displayToSourceOffset(
  map: PrettySourceMap,
  dispOffset: number,
  bias: "left" | "right" | "insert" = "left",
): number {
  const runs = map.runs;
  if (runs.length === 0) return 0;
  const clamped = Math.max(0, Math.min(dispOffset, map.displayLength));

  let lo = 0, hi = runs.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1;
    if ((map.dispStarts[mid] ?? 0) < clamped) lo = mid + 1;
    else hi = mid - 1;
  }

  const startIdx = Math.max(0, hi);
  for (let i = startIdx; i < runs.length; i++) {
    const run = runs[i]!;
    if (run.dispFrom > clamped) break;
    if (run.kind === "virtual-layout" && run.dispFrom === clamped && run.dispTo === clamped) {
      return bias === "left" ? run.srcFrom : bias === "right" ? run.srcTo : run.insertOffset;
    }
    if (run.dispFrom < clamped && clamped < run.dispTo) {
      if (run.kind === "virtual-layout") {
        return bias === "left" ? run.srcFrom : bias === "right" ? run.srcTo : run.insertOffset;
      }
      return run.srcFrom + (clamped - run.dispFrom);
    }
  }

  for (let i = startIdx; i < runs.length; i++) {
    const run = runs[i]!;
    if (run.dispFrom > clamped) break;
    if (run.kind !== "virtual-layout" && run.dispFrom <= clamped && clamped <= run.dispTo) {
      return run.srcFrom + (clamped - run.dispFrom);
    }
  }

  for (let i = startIdx; i < runs.length; i++) {
    const run = runs[i]!;
    if (run.dispFrom > clamped) break;
    if (run.dispFrom <= clamped && clamped <= run.dispTo) {
      if (clamped === run.dispFrom) return run.srcFrom;
      if (clamped === run.dispTo) return run.srcTo;
      return bias === "left" ? run.srcFrom : bias === "right" ? run.srcTo : run.insertOffset;
    }
  }

  return runs[runs.length - 1]!.srcTo;
}

/** O(log N) conversion from raw source UTF-16 offset to display UTF-16 offset. */
export function sourceToDisplayOffset(
  map: PrettySourceMap,
  srcOffset: number,
  bias: "left" | "right" = "left",
): number {
  const runs = map.runs;
  if (runs.length === 0) return 0;
  const clamped = Math.max(0, Math.min(srcOffset, map.rawLength));

  let lo = 0, hi = runs.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1;
    if ((map.srcStarts[mid] ?? 0) < clamped) lo = mid + 1;
    else hi = mid - 1;
  }

  const startIdx = Math.max(0, hi);
  let firstMatch: PrettyOriginRun | undefined;
  let lastMatch: PrettyOriginRun | undefined;
  for (let i = startIdx; i < runs.length; i++) {
    const run = runs[i]!;
    if (run.srcFrom > clamped) break;
    if (run.srcFrom <= clamped && clamped <= run.srcTo) {
      if (!firstMatch) firstMatch = run;
      lastMatch = run;
    }
  }

  const chosen = bias === "left" ? firstMatch : lastMatch;
  if (!chosen) return bias === "left" ? 0 : map.displayLength;
  if (chosen.kind === "virtual-layout") {
    if (chosen.srcFrom < chosen.srcTo) {
      if (clamped === chosen.srcFrom) return chosen.dispFrom;
      if (clamped === chosen.srcTo) return chosen.dispTo;
    }
    return bias === "left" ? chosen.dispFrom : chosen.dispTo;
  }
  return chosen.dispFrom + (clamped - chosen.srcFrom);
}

export function buildPrettySourceMap(
  sourceRevision: number,
  rawText: string,
  displayText: string,
  runs: readonly PrettyOriginRun[],
): PrettySourceMap {
  const normalizedRuns: readonly PrettyOriginRun[] = runs.length > 0 ? runs : [{
    kind: "identity", dispFrom: 0, dispTo: displayText.length,
    srcFrom: 0, srcTo: rawText.length, insertOffset: 0, segmentIndex: 0,
  }];

  const dispStarts = new Int32Array(normalizedRuns.length);
  const srcStarts = new Int32Array(normalizedRuns.length);
  for (let i = 0; i < normalizedRuns.length; i++) {
    const run = normalizedRuns[i]!;
    dispStarts[i] = run.dispFrom;
    srcStarts[i] = run.srcFrom;
  }

  const tempMap: PrettySourceMap = {
    sourceRevision, rawLength: rawText.length, displayLength: displayText.length,
    runs: normalizedRuns, dispStarts, srcStarts, displayLines: [],
  };

  const rawLineStarts = buildLineStarts(rawText);
  const dispLineStarts = buildLineStarts(displayText);
  const displayLines: PrettyDisplayLineInfo[] = new Array(dispLineStarts.length);
  let prevSourceLine = -1;

  for (let i = 0; i < dispLineStarts.length; i++) {
    const dispFrom = dispLineStarts[i] ?? 0;
    const dispLineEnd = i + 1 < dispLineStarts.length
      ? Math.max(dispFrom, (dispLineStarts[i + 1] ?? 0) - 1)
      : displayText.length;
    let srcAnchor = displayToSourceOffset(tempMap, dispFrom, "right");
    if (dispFrom < dispLineEnd) {
      let lo = 0, hi = normalizedRuns.length - 1;
      while (lo <= hi) {
        const mid = (lo + hi) >>> 1;
        if ((dispStarts[mid] ?? 0) <= dispFrom) lo = mid + 1;
        else hi = mid - 1;
      }
      const r = normalizedRuns[Math.max(0, hi)];
      if (r && r.kind === "virtual-layout" && r.dispFrom <= dispFrom && dispFrom < r.dispTo) {
        srcAnchor = r.srcTo;
      }
    }
    const sourceLine = findLineNumber(rawLineStarts, srcAnchor);
    const isContinuation = sourceLine === prevSourceLine;
    prevSourceLine = sourceLine;
    displayLines[i] = { sourceLine, isContinuation, dispFrom, srcAnchor };
  }

  return {
    sourceRevision, rawLength: rawText.length, displayLength: displayText.length,
    runs: normalizedRuns, dispStarts, srcStarts, displayLines,
  };
}

export function createIdentitySourceMap(
  sourceRevision: number,
  rawText: string,
  kind: "identity" | "raw-fallback" = "identity",
): PrettySourceMap {
  return buildPrettySourceMap(sourceRevision, rawText, rawText, [{
    kind, dispFrom: 0, dispTo: rawText.length,
    srcFrom: 0, srcTo: rawText.length, insertOffset: 0, segmentIndex: 0,
  }]);
}

/** Converts 1-based raw (line, col) to display (line, col, offset). */
export function sourceLineColToDisplay(
  map: PrettySourceMap,
  rawText: string,
  line: number,
  col = 1,
  bias: "left" | "right" = "right",
): PrettyLineCol & { readonly srcOffset: number } {
  const rawLineStarts = buildLineStarts(rawText);
  const clampedLine = Math.max(1, Math.min(Math.floor(line), rawLineStarts.length));
  const lineStart = rawLineStarts[clampedLine - 1] ?? 0;
  const lineEnd = clampedLine < rawLineStarts.length
    ? Math.max(lineStart, (rawLineStarts[clampedLine] ?? rawText.length) - 1)
    : rawText.length;
  let srcOffset = Math.min(lineEnd, lineStart + Math.max(0, Math.floor(col) - 1));
  if (srcOffset > lineStart && srcOffset < lineEnd) {
    const code = rawText.charCodeAt(srcOffset), prev = rawText.charCodeAt(srcOffset - 1);
    if (code >= 0xdc00 && code <= 0xdfff && prev >= 0xd800 && prev <= 0xdbff) {
      srcOffset = bias === "left" ? srcOffset - 1 : Math.min(lineEnd, srcOffset + 1);
    }
  }

  const dispOffset = sourceToDisplayOffset(map, srcOffset, bias);
  let lo = 0, hi = map.displayLines.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1;
    const info = map.displayLines[mid];
    if (info && info.dispFrom <= dispOffset) lo = mid + 1;
    else hi = mid - 1;
  }
  const dispLineIndex = Math.max(0, hi);
  const dispLineFrom = map.displayLines[dispLineIndex]?.dispFrom ?? 0;
  return { line: dispLineIndex + 1, col: dispOffset - dispLineFrom + 1, offset: dispOffset, srcOffset };
}

/** Converts display UTF-16 offset to 1-based raw (line, col, offset). */
export function displayOffsetToSourceLineCol(
  map: PrettySourceMap,
  rawText: string,
  dispOffset: number,
  bias: "left" | "right" | "insert" = "left",
): PrettyLineCol {
  let srcOffset = displayToSourceOffset(map, dispOffset, bias);
  if (srcOffset > 0 && srcOffset < rawText.length) {
    const code = rawText.charCodeAt(srcOffset), prev = rawText.charCodeAt(srcOffset - 1);
    if (code >= 0xdc00 && code <= 0xdfff && prev >= 0xd800 && prev <= 0xdbff) {
      srcOffset = bias === "right" ? srcOffset + 1 : srcOffset - 1;
    }
  }
  const rawLineStarts = buildLineStarts(rawText);
  const line = findLineNumber(rawLineStarts, srcOffset);
  const lineStart = rawLineStarts[line - 1] ?? 0;
  return { line, col: srcOffset - lineStart + 1, offset: srcOffset };
}

/** Builds a monotonic Unicode scalar (rune) <-> UTF-16 offset lookup table. */
export function createRuneOffsetTable(text: string): PrettyRuneOffsetTable {
  let runeCount = 0;
  for (let i = 0; i < text.length; runeCount += 1) {
    const code = text.charCodeAt(i);
    const next = i + 1 < text.length ? text.charCodeAt(i + 1) : 0;
    i += code >= 0xd800 && code <= 0xdbff && next >= 0xdc00 && next <= 0xdfff ? 2 : 1;
  }
  const runeToUtf16 = new Int32Array(runeCount + 1);
  let r = 0;
  for (let i = 0; i < text.length; ) {
    runeToUtf16[r++] = i;
    const code = text.charCodeAt(i);
    const next = i + 1 < text.length ? text.charCodeAt(i + 1) : 0;
    i += code >= 0xd800 && code <= 0xdbff && next >= 0xdc00 && next <= 0xdfff ? 2 : 1;
  }
  runeToUtf16[r] = text.length;

  const runeToUtf16Offset = (runeOffset: number): number => {
    const clamped = Math.max(0, Math.min(Math.floor(runeOffset), runeCount));
    return runeToUtf16[clamped] ?? text.length;
  };
  const utf16ToRuneOffset = (utf16Offset: number, bias: "left" | "right" = "left"): number => {
    const clamped = Math.max(0, Math.min(Math.floor(utf16Offset), text.length));
    let lo = 0, hi = runeCount;
    while (lo <= hi) {
      const mid = (lo + hi) >>> 1;
      const u = runeToUtf16[mid] ?? 0;
      if (u === clamped) return mid;
      if (u < clamped) lo = mid + 1;
      else hi = mid - 1;
    }
    return bias === "left" ? Math.max(0, hi) : Math.min(runeCount, lo);
  };
  return {
    runeCount, utf16Length: text.length, runeToUtf16, runeToUtf16Offset, utf16ToRuneOffset,
    runeRangeToUtf16(startRune: number, endRune: number): PrettyUtf16Range {
      const from = runeToUtf16Offset(startRune);
      return { from, to: Math.max(from, runeToUtf16Offset(endRune)) };
    },
  };
}

/** Projects a raw UTF-16 range [srcFrom, srcTo) conservatively into display UTF-16 coordinates. */
export function mapSourceUtf16RangeToDisplay(
  map: PrettySourceMap,
  srcFrom: number,
  srcTo: number,
): PrettyUtf16Range {
  const clampedFrom = Math.max(0, Math.min(srcFrom, map.rawLength));
  const clampedTo = Math.max(clampedFrom, Math.min(srcTo, map.rawLength));
  if (clampedFrom === clampedTo) {
    return {
      from: sourceToDisplayOffset(map, clampedFrom, "left"),
      to: sourceToDisplayOffset(map, clampedTo, "right"),
    };
  }
  let firstOverlap: PrettyOriginRun | undefined;
  let lastOverlap: PrettyOriginRun | undefined;
  for (const run of map.runs) {
    if (run.srcFrom >= clampedTo) break;
    if (run.srcTo > clampedFrom && run.srcFrom < clampedTo) {
      if (!firstOverlap) firstOverlap = run;
      lastOverlap = run;
    }
  }
  if (!firstOverlap || !lastOverlap) {
    const pos = sourceToDisplayOffset(map, clampedFrom, "left");
    return { from: pos, to: pos };
  }
  const dispFrom = firstOverlap.kind === "virtual-layout"
    ? firstOverlap.dispFrom
    : firstOverlap.dispFrom + Math.max(0, clampedFrom - firstOverlap.srcFrom);
  const dispTo = lastOverlap.kind === "virtual-layout"
    ? lastOverlap.dispTo
    : lastOverlap.dispFrom + Math.min(lastOverlap.srcTo - lastOverlap.srcFrom, clampedTo - lastOverlap.srcFrom);
  return { from: dispFrom, to: Math.max(dispFrom, dispTo) };
}

/** Projects SecretScreen (`{start, end}`) or RedactedSpan (`{start, length}`) rune spans to display ranges. */
export function mapRuneSpansToDisplay<T extends PrettyInputRuneSpan>(
  map: PrettySourceMap,
  rawTextOrTable: string | PrettyRuneOffsetTable,
  spans: readonly T[],
): PrettyProjectedRuneSpan<T>[] {
  if (spans.length === 0) return [];
  const table = typeof rawTextOrTable === "string" ? createRuneOffsetTable(rawTextOrTable) : rawTextOrTable;
  return spans.map((span) => {
    const startRune = span.start;
    const endRune = "end" in span ? span.end : span.start + span.length;
    const { from: srcFrom, to: srcTo } = table.runeRangeToUtf16(startRune, endRune);
    const { from: dispFrom, to: dispTo } = mapSourceUtf16RangeToDisplay(map, srcFrom, srcTo);
    return { span, srcFrom, srcTo, dispFrom, dispTo, from: dispFrom, to: dispTo };
  });
}
