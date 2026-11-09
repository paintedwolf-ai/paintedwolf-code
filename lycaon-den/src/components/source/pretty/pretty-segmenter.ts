import {
  classifyDocumentFormat,
  isClosingFence,
  isEnvelopeHeaderLine,
  isExcerptBarrierLine,
  isJsonFenceLang,
  type LineSpan,
  matchEnvelopeOrLogPrefix,
  parseOpeningFence,
  splitLines,
} from "./pretty-classify.ts";
import {
  countNewlinesSlice,
  PrettyBudgetTracker,
  utf8ByteLengthSlice,
} from "./pretty-limits.ts";
import { buildPrettySourceMap, createIdentitySourceMap } from "./pretty-map.ts";
import { printJsonSegment } from "./pretty-printer.ts";
import type {
  PrettyFormatOptions,
  PrettyOriginRun,
  PrettyOriginRunKind,
  PrettyProjectionOutcome,
  PrettyProjectionResult,
  PrettyRawRange,
  PrettySegment,
  PrettySegmentKind,
} from "./pretty-types.ts";

function overlapsRawRanges(
  srcFrom: number,
  srcTo: number,
  ranges?: readonly PrettyRawRange[],
): boolean {
  if (!ranges || ranges.length === 0) return false;
  for (const r of ranges) {
    if (r.srcFrom === r.srcTo) {
      if (srcFrom <= r.srcFrom && r.srcFrom <= srcTo) return true;
    } else if (r.srcFrom < srcTo && r.srcTo > srcFrom) {
      return true;
    }
  }
  return false;
}

function findMultiLineJsonEndLine(
  rawText: string,
  lines: readonly LineSpan[],
  startLineIdx: number,
  jsonStartOffset: number,
  budget: PrettyBudgetTracker,
): number | "budget" | null {
  let depth = 0, inString = false, escaped = false;
  for (let idx = startLineIdx; idx < lines.length; idx++) {
    const line = lines[idx]!;
    if (idx > startLineIdx) {
      const lineText = rawText.slice(line.lineFrom, line.lineTo);
      if (isExcerptBarrierLine(lineText) || parseOpeningFence(lineText) !== null) return null;
    }
    const scanFrom = idx === startLineIdx ? jsonStartOffset : line.lineFrom;
    for (let p = scanFrom; p < line.lineTo; p++) {
      if (!budget.recordVisit(1)) return "budget";
      const c = rawText.charCodeAt(p);
      if (inString) {
        if (escaped) escaped = false;
        else if (c === 0x5c) escaped = true;
        else if (c === 0x22) inString = false;
        else if (c < 0x20) return null;
        continue;
      }
      if (c === 0x22) inString = true;
      else if (c === 0x7b || c === 0x5b) depth += 1;
      else if (c === 0x7d || c === 0x5d) {
        depth -= 1;
        if (depth === 0) {
          for (let k = p + 1; k < line.lineTo; k++) {
            const rem = rawText.charCodeAt(k);
            if (rem !== 0x20 && rem !== 0x09) return null;
          }
          return idx;
        }
      }
    }
  }
  return null;
}

function buildFallbackResult(
  rawText: string,
  options: PrettyFormatOptions,
  budget: PrettyBudgetTracker,
  outcome: PrettyProjectionOutcome,
  runKind: "identity" | "raw-fallback" = "identity",
  language?: "json",
): PrettyProjectionResult {
  const sourceMap = createIdentitySourceMap(options.sourceRevision, rawText, runKind);
  budget.counters.outputUtf16Units = rawText.length;
  budget.counters.outputUtf8Bytes = budget.counters.inputUtf8Bytes;
  budget.counters.outputLines = sourceMap.displayLines.length;
  budget.counters.mappingRuns = sourceMap.runs.length;
  const segments: PrettySegment[] = rawText.length > 0 ? [{
    kind: "raw", srcFrom: 0, srcTo: rawText.length,
    dispFrom: 0, dispTo: rawText.length, formatted: false,
    ...(language ? { language } : {}),
  }] : [];
  return {
    sourceIdentity: options.sourceIdentity,
    sourceRevision: options.sourceRevision,
    outcome,
    ...(outcome === "limit-exceeded" && budget.limitReason ? { limitReason: budget.limitReason } : {}),
    displayText: rawText,
    sourceMap,
    segments,
    counters: budget.counters,
  };
}

export function formatPrettyProjection(
  rawText: string,
  options: PrettyFormatOptions,
): PrettyProjectionResult {
  const indentUnit = options.indentUnit ?? "  ";
  const budget = new PrettyBudgetTracker(rawText, options.limits, options.signal, options.shouldCancel);
  if (!budget.checkInput()) {
    return buildFallbackResult(rawText, options, budget, budget.canceled ? "canceled" : "limit-exceeded");
  }
  if (rawText.length === 0) return buildFallbackResult(rawText, options, budget, "passthrough");

  const mode = classifyDocumentFormat(rawText, options.path, options.languageHint);
  if (mode === "code-passthrough") return buildFallbackResult(rawText, options, budget, "passthrough");

  const chunks: string[] = [];
  const runs: PrettyOriginRun[] = [];
  const segments: PrettySegment[] = [];
  let dispOffset = 0;
  let anyFormatted = false;

  const appendRawRun = (srcFrom: number, srcTo: number, kind: PrettyOriginRunKind, segIdx: number): boolean => {
    if (srcFrom >= srcTo) return true;
    const len = srcTo - srcFrom;
    if (!budget.recordOutputRun(len, utf8ByteLengthSlice(rawText, srcFrom, srcTo), countNewlinesSlice(rawText, srcFrom, srcTo))) {
      return false;
    }
    runs.push({ kind, dispFrom: dispOffset, dispTo: dispOffset + len, srcFrom, srcTo, insertOffset: srcFrom, segmentIndex: segIdx });
    chunks.push(rawText.slice(srcFrom, srcTo));
    dispOffset += len;
    return true;
  };

  const appendRawSegment = (srcFrom: number, srcTo: number, segKind: PrettySegmentKind, runKind: PrettyOriginRunKind, language?: "json"): boolean => {
    if (srcFrom >= srcTo) return true;
    const segIdx = segments.length, dispStart = dispOffset;
    if (!appendRawRun(srcFrom, srcTo, runKind, segIdx)) return false;
    segments.push({ kind: segKind, srcFrom, srcTo, dispFrom: dispStart, dispTo: dispOffset, formatted: false, ...(language ? { language } : {}) });
    return true;
  };

  const tryFormatJsonSlice = (srcFrom: number, srcTo: number, segKind: "json" | "jsonl-record"): "formatted" | "malformed" | "budget" => {
    if (overlapsRawRanges(srcFrom, srcTo, options.rawSegmentRanges)) return "malformed";
    let trimFrom = srcFrom, trimTo = srcTo;
    while (trimFrom < trimTo && rawText.charCodeAt(trimFrom) <= 0x20) trimFrom += 1;
    while (trimTo > trimFrom && rawText.charCodeAt(trimTo - 1) <= 0x20) trimTo -= 1;
    if (trimFrom >= trimTo) return "malformed";
    const open = rawText.charCodeAt(trimFrom), close = rawText.charCodeAt(trimTo - 1);
    if (!((open === 0x7b && close === 0x7d) || (open === 0x5b && close === 0x5d))) return "malformed";

    const savedRunsLen = runs.length, savedChunksLen = chunks.length, savedDisp = dispOffset;
    const savedUtf16 = budget.counters.outputUtf16Units, savedUtf8 = budget.counters.outputUtf8Bytes;
    const savedLines = budget.counters.outputLines, savedMapRuns = budget.counters.mappingRuns;
    const segIdx = segments.length;

    if (trimFrom > srcFrom && !appendRawRun(srcFrom, trimFrom, "identity", segIdx)) return "budget";
    const printed = printJsonSegment(rawText, trimFrom, trimTo, dispOffset, segIdx, indentUnit, budget);
    if (printed.status === "limit-or-canceled") return "budget";
    if (printed.status === "malformed") {
      runs.length = savedRunsLen; chunks.length = savedChunksLen; dispOffset = savedDisp;
      budget.counters.outputUtf16Units = savedUtf16; budget.counters.outputUtf8Bytes = savedUtf8;
      budget.counters.outputLines = savedLines; budget.counters.mappingRuns = savedMapRuns;
      return "malformed";
    }
    for (const r of printed.runs) runs.push(r);
    chunks.push(printed.displayText);
    dispOffset += printed.displayText.length;
    if (trimTo < srcTo && !appendRawRun(trimTo, srcTo, "identity", segIdx)) return "budget";
    segments.push({ kind: segKind, srcFrom, srcTo, dispFrom: savedDisp, dispTo: dispOffset, formatted: true, language: "json" });
    anyFormatted = true;
    return "formatted";
  };

  const formatJsonlLines = (lines: readonly LineSpan[]): boolean => {
    for (const line of lines) {
      if (rawText.slice(line.lineFrom, line.lineTo).trim().length === 0) {
        if (!appendRawSegment(line.lineFrom, line.lineTo, "raw", "identity")) return false;
      } else {
        const res = tryFormatJsonSlice(line.lineFrom, line.lineTo, "jsonl-record");
        if (res === "budget") return false;
        if (res === "malformed" && !appendRawSegment(line.lineFrom, line.lineTo, "raw", "raw-fallback", "json")) return false;
      }
      if (line.sepTo > line.sepFrom && !appendRawRun(line.sepFrom, line.sepTo, "record-delimiter", Math.max(0, segments.length - 1))) {
        return false;
      }
    }
    return true;
  };

  if (mode === "json") {
    const res = tryFormatJsonSlice(0, rawText.length, "json");
    if (res === "malformed") return buildFallbackResult(rawText, options, budget, "passthrough", "raw-fallback", "json");
  } else if (mode === "jsonl") {
    formatJsonlLines(splitLines(rawText));
  } else {
    const wholeTry = mode === "auto" && !overlapsRawRanges(0, rawText.length, options.rawSegmentRanges)
      ? tryFormatJsonSlice(0, rawText.length, "json")
      : "malformed";
    if (wholeTry === "malformed") {
      const lines = splitLines(rawText);
      const nonBlank = lines.filter((l) => rawText.slice(l.lineFrom, l.lineTo).trim().length > 0);
      const looksLikeJsonl = mode === "auto" && nonBlank.length >= 2 && nonBlank.every((l) => {
        const lineStr = rawText.slice(l.lineFrom, l.lineTo);
        const t = lineStr.trimStart();
        return (t.startsWith("{") || t.startsWith("[")) &&
          matchEnvelopeOrLogPrefix(lineStr) === null &&
          !isEnvelopeHeaderLine(lineStr);
      });
      if (looksLikeJsonl) {
        formatJsonlLines(lines);
      } else {
        for (let i = 0; i < lines.length && !budget.canceled && !budget.limitReason; i++) {
          const line = lines[i]!;
          const lineText = rawText.slice(line.lineFrom, line.lineTo);
          const fence = parseOpeningFence(lineText);
          if (fence) {
            let closeIdx = -1;
            for (let j = i + 1; j < lines.length; j++) {
              if (isClosingFence(rawText.slice(lines[j]!.lineFrom, lines[j]!.lineTo), fence.char, fence.len)) { closeIdx = j; break; }
            }
            if (closeIdx < 0) {
              appendRawSegment(line.lineFrom, lines[lines.length - 1]!.sepTo, "raw", "identity");
              break;
            }
            const closeLine = lines[closeIdx]!;
            const jsonFenceKind = isJsonFenceLang(fence.lang);
            appendRawSegment(line.lineFrom, line.sepTo, "fence-delimiter", "identity");
            if (closeIdx > i + 1) {
              const bodyStart = lines[i + 1]!.lineFrom, lastBodyLine = lines[closeIdx - 1]!;
              if (!jsonFenceKind) {
                appendRawSegment(bodyStart, lastBodyLine.sepTo, "raw", "identity");
              } else if (jsonFenceKind === "jsonl") {
                formatJsonlLines(lines.slice(i + 1, closeIdx));
              } else {
                const res = tryFormatJsonSlice(bodyStart, lastBodyLine.lineTo, "json");
                if (res === "malformed") appendRawSegment(bodyStart, lastBodyLine.lineTo, "raw", "raw-fallback", "json");
                if (res !== "budget") appendRawRun(lastBodyLine.sepFrom, lastBodyLine.sepTo, "identity", Math.max(0, segments.length - 1));
              }
            }
            appendRawSegment(closeLine.lineFrom, closeLine.sepTo, "fence-delimiter", "identity");
            i = closeIdx;
            continue;
          }
          if (mode === "markdown") {
            appendRawSegment(line.lineFrom, line.sepTo, "raw", "identity");
            continue;
          }
          const prefixOffset = matchEnvelopeOrLogPrefix(lineText);
          if (prefixOffset !== null) {
            const jsonStart = line.lineFrom + prefixOffset;
            if (!appendRawSegment(line.lineFrom, jsonStart, "envelope-header", "identity")) break;
            const endIdx = findMultiLineJsonEndLine(rawText, lines, i, jsonStart, budget);
            if (typeof endIdx === "number") {
              const endLine = lines[endIdx]!;
              if (tryFormatJsonSlice(jsonStart, endLine.lineTo, "json") === "formatted") {
                appendRawRun(endLine.sepFrom, endLine.sepTo, "identity", Math.max(0, segments.length - 1));
                i = endIdx;
                continue;
              }
            }
            if (!budget.canceled && !budget.limitReason) appendRawSegment(jsonStart, line.sepTo, "raw", "raw-fallback", "json");
            continue;
          }
          if (isEnvelopeHeaderLine(lineText)) {
            appendRawSegment(line.lineFrom, line.sepTo, "envelope-header", "identity");
            continue;
          }
          const trimmedStart = lineText.trimStart();
          const leadSpaces = lineText.length - trimmedStart.length;
          if (trimmedStart.startsWith("{") || trimmedStart.startsWith("[")) {
            const singleRes = tryFormatJsonSlice(line.lineFrom, line.lineTo, "json");
            if (singleRes === "formatted") {
              appendRawRun(line.sepFrom, line.sepTo, "identity", Math.max(0, segments.length - 1));
              continue;
            }
            if (singleRes === "budget") break;
            const endIdx = findMultiLineJsonEndLine(rawText, lines, i, line.lineFrom + leadSpaces, budget);
            if (typeof endIdx === "number") {
              const endLine = lines[endIdx]!;
              if (tryFormatJsonSlice(line.lineFrom, endLine.lineTo, "json") === "formatted") {
                appendRawRun(endLine.sepFrom, endLine.sepTo, "identity", Math.max(0, segments.length - 1));
                i = endIdx;
                continue;
              }
            }
            if (!budget.canceled && !budget.limitReason) appendRawSegment(line.lineFrom, line.sepTo, "raw", "raw-fallback", "json");
            continue;
          }
          appendRawSegment(line.lineFrom, line.sepTo, "raw", "identity");
        }
      }
    }
  }

  if (budget.canceled || budget.limitReason) {
    return buildFallbackResult(rawText, options, budget, budget.canceled ? "canceled" : "limit-exceeded");
  }

  const displayText = chunks.join("");
  const sourceMap = buildPrettySourceMap(options.sourceRevision, rawText, displayText, runs);
  return {
    sourceIdentity: options.sourceIdentity,
    sourceRevision: options.sourceRevision,
    outcome: anyFormatted ? "formatted" : "passthrough",
    displayText,
    sourceMap,
    segments,
    counters: budget.counters,
  };
}
