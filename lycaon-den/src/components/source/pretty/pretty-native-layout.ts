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
  determineDocumentLineEligibility,
  type DocumentFormatMode,
  modeToPrimaryFamily,
  type PrettyLayoutFamily,
} from "./pretty-classify.ts";
import {
  maxPrettyVisits,
  PrettyBudgetTracker,
  utf8ByteLength,
} from "./pretty-limits.ts";
import {
  continuationEqualsLineStart,
  continuationFromLineEnd,
  INITIAL_LINE_CONTINUATION,
  scanLineLexicalBoundaries,
} from "./pretty-line-scanners.ts";
import type {
  PrettyLayoutBoundary,
  PrettyLineContinuationState,
  PrettyLineLayoutState,
  PrettyNativeLayoutOptions,
  PrettyNativeLayoutResult,
  PrettyProjectionOutcome,
  PrettyProtectedSpan,
  PrettyProtectedSpanKind,
  PrettyResourceCounters,
  PrettyResourceLimits,
} from "./pretty-types.ts";

export {
  continuationEqualsLineStart,
  continuationFromLineEnd,
  INITIAL_LINE_CONTINUATION,
  scanLineLexicalBoundaries,
} from "./pretty-line-scanners.ts";

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

function appendLineDecorations(
  target: Range<Decoration>[],
  lineState: PrettyLineLayoutState,
  lineFrom: number,
  indentWidthCh: number,
): void {
  for (const b of lineState.relBoundaries) {
    const pos = lineFrom + b.relPos;
    target.push(SHARED_BREAK_DECORATION.range(pos));
    if (b.depth > 0) {
      target.push(getIndentDecoration(b.depth, indentWidthCh).range(pos));
    }
  }
}

const SYNTAX_XML_INTERIOR_NODES = new Set([
  "Comment",
  "Cdata",
  "AttributeValue",
]);

const SYNTAX_XML_FULL_NODES = new Set([
  "RawText",
  "ScriptText",
  "StyleText",
]);

function classifySyntaxProtectedNode(
  name: string,
): { readonly kind: PrettyProtectedSpanKind; readonly interiorOnly: boolean } | null {
  if (name === "String" || name === "PropertyName") {
    return { kind: "string", interiorOnly: false };
  }
  if (name === "RegExp") {
    return { kind: "regex", interiorOnly: false };
  }
  if (name === "TemplateString") {
    return { kind: "template", interiorOnly: false };
  }
  if (name === "BlockComment") {
    return { kind: "block-comment", interiorOnly: false };
  }
  if (name === "LineComment") {
    return { kind: "line-comment", interiorOnly: false };
  }
  if (SYNTAX_XML_INTERIOR_NODES.has(name)) {
    return { kind: "xml-protected", interiorOnly: true };
  }
  if (SYNTAX_XML_FULL_NODES.has(name)) {
    return { kind: "xml-protected", interiorOnly: false };
  }
  return null;
}

function pushMergedSpan(
  spans: PrettyProtectedSpan[],
  from: number,
  to: number,
  kind: PrettyProtectedSpanKind,
  openAtLineEnd: boolean,
): void {
  if (to <= from) return;
  const last = spans[spans.length - 1];
  if (last && from < last.to) {
    if (to > last.to) {
      spans[spans.length - 1] = {
        from: last.from,
        to,
        ...(last.kind !== undefined ? { kind: last.kind } : {}),
        openAtLineEnd,
      };
    }
    return;
  }
  spans.push({ from, to, kind, openAtLineEnd });
}

function formatSyntaxSpanSignature(
  syntaxNodes: number,
  spans: readonly PrettyProtectedSpan[],
): string {
  if (syntaxNodes === 0 && spans.length === 0) return "";
  let out = `${syntaxNodes}|`;
  for (let i = 0; i < spans.length; i++) {
    const s = spans[i]!;
    if (i > 0) out += ",";
    out += `${s.from}:${s.to}:${s.kind ?? ""}:${s.openAtLineEnd ? 1 : 0}`;
  }
  return out;
}

/**
 * Inspects CodeMirror's `syntaxTree(state)` over `[lineFrom, lineTo]` to:
 * 1. Account for every syntax node starting on this line (`lineFrom <= node.from <= lineTo`)
 *    under `PrettyBudgetTracker`.
 * 2. Collect line-relative protected spans for literal, comment, regex, CDATA, and raw-text
 *    nodes so structural formatting never splits inside syntax-tree literals or comments,
 *    and interior characters of protected spans never mutate fallback lexical state.
 */
function collectLineSyntaxProtectedSpans(
  state: EditorState | undefined,
  lineFrom: number,
  lineTo: number,
  budget: PrettyBudgetTracker,
): {
  readonly spans: readonly PrettyProtectedSpan[];
  readonly syntaxNodes: number;
  readonly syntaxVisits: number;
  readonly syntaxSpanSignature: string;
} | "budget" {
  if (!state) {
    return {
      spans: [],
      syntaxNodes: 0,
      syntaxVisits: 0,
      syntaxSpanSignature: "",
    };
  }
  const tree = syntaxTree(state);
  if (tree.length === 0) {
    return {
      spans: [],
      syntaxNodes: 0,
      syntaxVisits: 0,
      syntaxSpanSignature: "",
    };
  }
  const spans: PrettyProtectedSpan[] = [];
  const lineLen = lineTo - lineFrom;
  let syntaxNodes = 0;
  const visitsBefore = budget.counters.visits;
  let withinBudget = true;

  tree.iterate({
    from: lineFrom,
    to: lineTo,
    enter: (node) => {
      if (node.from >= lineFrom && node.from <= lineTo) {
        if (!budget.recordSyntaxNode(1)) {
          withinBudget = false;
          return false;
        }
        syntaxNodes += 1;
      }
      const classified = classifySyntaxProtectedNode(node.name);
      if (classified !== null) {
        const pFrom = Math.max(
          0,
          (classified.interiorOnly ? node.from + 1 : node.from) - lineFrom,
        );
        const pTo = Math.min(
          lineLen,
          (classified.interiorOnly ? node.to - 1 : node.to) - lineFrom,
        );
        const openAtLineEnd = node.to > lineTo;
        pushMergedSpan(spans, pFrom, pTo, classified.kind, openAtLineEnd);
      }
      return undefined;
    },
  });

  if (!withinBudget) return "budget";
  return {
    spans,
    syntaxNodes,
    syntaxVisits: budget.counters.visits - visitsBefore,
    syntaxSpanSignature: formatSyntaxSpanSignature(syntaxNodes, spans),
  };
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
  let runningCont: PrettyLineContinuationState = INITIAL_LINE_CONTINUATION;

  for (let lineNum = 1; lineNum <= totalLines; lineNum++) {
    const line = doc.line(lineNum);
    const elig = eligibilities[lineNum - 1]!;
    const startCont = elig.resetDepth ? INITIAL_LINE_CONTINUATION : runningCont;

    const syntaxRes = collectLineSyntaxProtectedSpans(
      editorState,
      line.from,
      line.to,
      budget,
    );
    if (syntaxRes === "budget") {
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

    const scanned = scanLineLexicalBoundaries(
      line.text,
      lineNum,
      totalLines,
      line.from,
      startCont,
      elig.eligible,
      elig.scanStartCol,
      budget,
      elig.family,
      startCont.inBlockComment,
      startCont.xmlRawTag,
      syntaxRes.spans,
      undefined,
      syntaxRes.syntaxNodes,
      syntaxRes.syntaxVisits,
      syntaxRes.syntaxSpanSignature,
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
    runningCont = continuationFromLineEnd(scanned);
    for (const b of scanned.relBoundaries) {
      boundaries.push({ pos: line.from + b.relPos, depth: b.depth });
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
  readonly lastIncrementalSuffixLinesReused: number;
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
      lastIncrementalSuffixLinesReused: 0,
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
      lastIncrementalSuffixLinesReused: 0,
    };
  }

  const ranges: Range<Decoration>[] = [];
  for (let lineNum = 1; lineNum <= res.lineStates.length; lineNum++) {
    const ls = res.lineStates[lineNum - 1]!;
    if (ls.relBoundaries.length > 0) {
      appendLineDecorations(ranges, ls, doc.line(lineNum).from, indentWidthCh);
    }
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
    lastIncrementalSuffixLinesReused: 0,
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
 * Incrementally updates line layout states, CodeMirror decorations, and whole-document
 * resource counters across changed line ranges (`tr.changes.iterChangedRanges`),
 * stopping as soon as multiline continuation state and `syntaxSpanSignature` converge
 * with unchanged suffix lines. Reuses unchanged prefix/suffix `PrettyLineLayoutState`
 * objects by reference (avoiding per-line `doc.line()` lookups, syntax-tree walks,
 * character scans, and object allocations on unchanged suffix lines) while performing
 * bounded O(totalLines) array-reference copying (`slice` + spread) and, only when a
 * replaced line previously held `maxDepthReached`, a bounded O(totalLines) max-depth scan.
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
    newDoc.length > 0 &&
    INCREMENTAL_CAPABLE_MODES.has(prev.mode) &&
    prev.lineStates.length === oldDoc.lines &&
    prev.outcome !== "limit-exceeded" &&
    prev.outcome !== "canceled";

  if (!canIncremental) {
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

  if (!hasChange) {
    if (syntaxTree(tr.state) !== syntaxTree(tr.startState)) {
      return buildFieldStateFromScratch(newDoc, config, tr.state);
    }
    return prev;
  }

  const oldStartLine = oldDoc.lineAt(minFromA).number;
  const newStartLine = newDoc.lineAt(minFromB).number;
  const newEndLine = newDoc.lineAt(Math.min(newDoc.length, maxToB)).number;
  const totalLines = newDoc.lines;
  const lineDelta = totalLines - oldDoc.lines;

  const rescanStartLine = Math.max(1, Math.min(oldStartLine, newStartLine) - 1);
  const rescanMinEndLine = Math.min(totalLines, newEndLine + 1);
  const oldRescanMinEndLine = Math.min(
    oldDoc.lines,
    rescanMinEndLine - lineDelta,
  );

  let inputUtf8Bytes = prev.counters.inputUtf8Bytes + lineDelta;
  let outputUtf8Delta =
    prev.counters.outputUtf8Bytes - prev.counters.inputUtf8Bytes;
  let outputUtf16Delta = prev.counters.outputUtf16Units - oldDoc.length;
  let outputLinesDelta = prev.counters.outputLines - oldDoc.lines;
  let tokens = prev.counters.tokens;
  let mappingRuns = prev.counters.mappingRuns;
  let syntaxNodesVisited = prev.counters.syntaxNodesVisited;
  let visits = prev.counters.visits;
  let oldRemovedHitMaxDepth = false;

  for (let oldL = rescanStartLine; oldL <= oldRescanMinEndLine; oldL++) {
    const m = prev.lineStates[oldL - 1]!.metrics;
    inputUtf8Bytes -= m.lineUtf8Bytes;
    outputUtf8Delta -= m.outputUtf8Delta;
    outputUtf16Delta -= m.outputUtf16Delta;
    outputLinesDelta -= m.outputLinesDelta;
    tokens -= m.tokens;
    mappingRuns -= m.mappingRuns;
    syntaxNodesVisited -= m.syntaxNodes;
    visits -= m.visits;
    if (m.maxDepth >= prev.counters.maxDepthReached) {
      oldRemovedHitMaxDepth = true;
    }
  }

  const initialSpanLen = rescanMinEndLine - rescanStartLine + 1;
  const initialLineUtf8: number[] = new Array(initialSpanLen);
  for (let i = 0; i < initialSpanLen; i++) {
    const l = rescanStartLine + i;
    const bytes = utf8ByteLength(newDoc.line(l).text);
    initialLineUtf8[i] = bytes;
    inputUtf8Bytes += bytes;
  }

  const budget = new PrettyBudgetTracker("", {
    ...prev.limits,
    maxVisits: prev.limits?.maxVisits ?? maxPrettyVisits(newDoc.length),
  });
  budget.counters.inputUtf16Units = newDoc.length;
  budget.counters.inputUtf8Bytes = inputUtf8Bytes;
  budget.counters.outputUtf16Units = newDoc.length + outputUtf16Delta;
  budget.counters.outputUtf8Bytes = inputUtf8Bytes + outputUtf8Delta;
  budget.counters.outputLines = totalLines + outputLinesDelta;
  budget.counters.tokens = tokens;
  budget.counters.mappingRuns = mappingRuns;
  budget.counters.syntaxNodesVisited = syntaxNodesVisited;
  budget.counters.visits = visits;
  budget.counters.maxDepthReached = oldRemovedHitMaxDepth
    ? 0
    : prev.counters.maxDepthReached;

  if (!budget.checkInput() || totalLines > budget.limits.maxOutputLines) {
    return buildFieldStateFromScratch(newDoc, config, tr.state);
  }

  const isJsonl = prev.mode === "jsonl";
  const lineFamily = prev.mode as PrettyLayoutFamily;
  let runningCont: PrettyLineContinuationState =
    rescanStartLine === 1 || isJsonl
      ? INITIAL_LINE_CONTINUATION
      : continuationFromLineEnd(prev.lineStates[rescanStartLine - 2]!);

  let actualRescanEndLine = rescanMinEndLine;
  const rescannedStates: PrettyLineLayoutState[] = [];
  const addedDecorations: Range<Decoration>[] = [];

  for (let l = rescanStartLine; l <= totalLines; l++) {
    const startCont = isJsonl || l === 1 ? INITIAL_LINE_CONTINUATION : runningCont;
    const line = newDoc.line(l);
    const savedSyntaxNodes = budget.counters.syntaxNodesVisited;
    const savedVisits = budget.counters.visits;

    const syntaxRes = collectLineSyntaxProtectedSpans(
      tr.state,
      line.from,
      line.to,
      budget,
    );
    if (syntaxRes === "budget") {
      return buildFieldStateFromScratch(newDoc, config, tr.state);
    }

    if (l > rescanMinEndLine) {
      const oldLineNum = l - lineDelta;
      const oldState = prev.lineStates[oldLineNum - 1];
      const oldWasLast = oldLineNum === oldDoc.lines;
      const newIsLast = l === totalLines;
      if (
        oldState &&
        continuationEqualsLineStart(startCont, oldState) &&
        oldState.syntaxSpanSignature === syntaxRes.syntaxSpanSignature &&
        oldWasLast === newIsLast
      ) {
        budget.counters.syntaxNodesVisited = savedSyntaxNodes;
        budget.counters.visits = savedVisits;
        actualRescanEndLine = l - 1;
        break;
      }
      if (oldState) {
        const m = oldState.metrics;
        budget.counters.outputUtf8Bytes -= m.outputUtf8Delta;
        budget.counters.outputUtf16Units -= m.outputUtf16Delta;
        budget.counters.outputLines -= m.outputLinesDelta;
        budget.counters.tokens -= m.tokens;
        budget.counters.mappingRuns -= m.mappingRuns;
        budget.counters.syntaxNodesVisited -= m.syntaxNodes;
        budget.counters.visits -= m.visits;
        if (m.maxDepth >= prev.counters.maxDepthReached) {
          oldRemovedHitMaxDepth = true;
        }
      }
    }

    const lineUtf8 =
      l <= rescanMinEndLine
        ? initialLineUtf8[l - rescanStartLine]!
        : (prev.lineStates[l - lineDelta - 1]?.metrics.lineUtf8Bytes ??
          utf8ByteLength(line.text));

    const scanned = scanLineLexicalBoundaries(
      line.text,
      l,
      totalLines,
      line.from,
      startCont,
      true,
      0,
      budget,
      lineFamily,
      startCont.inBlockComment,
      startCont.xmlRawTag,
      syntaxRes.spans,
      lineUtf8,
      syntaxRes.syntaxNodes,
      syntaxRes.syntaxVisits,
      syntaxRes.syntaxSpanSignature,
    );
    if (scanned === "budget") {
      return buildFieldStateFromScratch(newDoc, config, tr.state);
    }

    actualRescanEndLine = l;
    rescannedStates.push(scanned);
    runningCont = continuationFromLineEnd(scanned);
    appendLineDecorations(
      addedDecorations,
      scanned,
      line.from,
      prev.indentWidthCh,
    );
  }

  if (
    budget.counters.outputLines > budget.limits.maxOutputLines ||
    budget.counters.outputUtf16Units > budget.limits.maxOutputUtf16Units ||
    budget.counters.outputUtf8Bytes > budget.limits.maxOutputUtf8Bytes ||
    budget.counters.mappingRuns > budget.limits.maxMappingRuns ||
    budget.counters.tokens > budget.limits.maxTokens ||
    budget.counters.visits > budget.visitCeiling
  ) {
    return buildFieldStateFromScratch(newDoc, config, tr.state);
  }

  const oldReplacedEndLine = Math.min(
    oldDoc.lines,
    actualRescanEndLine - lineDelta,
  );
  const nextLineStates = [
    ...prev.lineStates.slice(0, rescanStartLine - 1),
    ...rescannedStates,
    ...prev.lineStates.slice(oldReplacedEndLine),
  ];

  if (oldRemovedHitMaxDepth) {
    let maxD = 0;
    for (let i = 0; i < nextLineStates.length; i++) {
      const d = nextLineStates[i]!.metrics.maxDepth;
      if (d > maxD) maxD = d;
    }
    budget.counters.maxDepthReached = maxD;
  }

  const filterFrom = newDoc.line(rescanStartLine).from;
  const filterTo = newDoc.line(actualRescanEndLine).to;
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
    counters: budget.counters,
    lastIncrementalLinesScanned: rescannedStates.length,
    lastIncrementalSuffixLinesReused: totalLines - actualRescanEndLine,
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
    const treeChanged = syntaxTree(tr.state) !== syntaxTree(tr.startState);
    if (nextConfig !== null) {
      const sameConfig =
        prev.enabled === nextConfig.enabled &&
        prev.path === nextConfig.path &&
        prev.languageHint === nextConfig.languageHint &&
        prev.indentWidthCh === (nextConfig.indentWidthCh ?? 2) &&
        prev.limits === nextConfig.limits;
      if (sameConfig && !tr.docChanged && !treeChanged) {
        return prev;
      }
      return buildFieldStateFromScratch(tr.state.doc, nextConfig, tr.state);
    }
    if (!prev.enabled) return prev;
    if (tr.docChanged || treeChanged) {
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
