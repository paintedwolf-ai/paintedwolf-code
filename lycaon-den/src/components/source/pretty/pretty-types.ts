import type {
  DocumentFormatMode,
  PrettyLayoutFamily,
} from "./pretty-classify.ts";

export type { DocumentFormatMode, PrettyLayoutFamily };

export interface PrettyResourceLimits {
  readonly maxInputUtf8Bytes: number;
  readonly maxInputUtf16Units: number;
  readonly maxOutputUtf8Bytes: number;
  readonly maxOutputUtf16Units: number;
  readonly maxOutputLines: number;
  readonly maxNestingDepth: number;
  readonly maxTokens: number;
  readonly maxMappingRuns: number;
  readonly maxVisits?: number;
  readonly maxCacheBytes: number;
}

export interface PrettyResourceCounters {
  inputUtf8Bytes: number;
  inputUtf16Units: number;
  outputUtf8Bytes: number;
  outputUtf16Units: number;
  outputLines: number;
  maxDepthReached: number;
  tokens: number;
  mappingRuns: number;
  syntaxNodesVisited: number;
  visits: number;
}

export type PrettyProjectionOutcome =
  | "formatted"
  | "passthrough"
  | "limit-exceeded"
  | "canceled";

export interface PrettyUtf16Range {
  readonly from: number;
  readonly to: number;
}

export interface PrettyRuneOffsetTable {
  readonly runeCount: number;
  readonly utf16Length: number;
  readonly runeToUtf16: Int32Array;
  runeToUtf16Offset(runeOffset: number): number;
  utf16ToRuneOffset(utf16Offset: number, bias?: "left" | "right"): number;
  runeRangeToUtf16(startRune: number, endRune: number): PrettyUtf16Range;
}

export type PrettyInputRuneSpan =
  | { readonly start: number; readonly end: number }
  | { readonly start: number; readonly length: number };

/**
 * Source-native lexical boundary on the raw CodeMirror document (Decisions 004 & 005).
 * At `pos`, a zero-height block `Break` widget (`side: -1`) is emitted, and when
 * `depth > 0`, an inline `Indent` widget (`side: 2`) is emitted at the same `pos`.
 */
export interface PrettyLayoutBoundary {
  readonly pos: number;
  readonly depth: number;
}

export interface PrettyLineRelativeBoundary {
  readonly relPos: number;
  readonly depth: number;
}

export interface PrettyLineContinuationState {
  readonly depth: number;
  readonly inBlockComment: boolean;
  readonly inXmlCdata: boolean;
  readonly xmlRawTag: string | null;
  readonly quoteCode: number;
  readonly lastSigCode: number;
  readonly delimSignature: string;
}

export interface PrettyLineResourceMetrics {
  readonly lineUtf8Bytes: number;
  readonly outputUtf16Delta: number;
  readonly outputUtf8Delta: number;
  readonly outputLinesDelta: number;
  readonly mappingRuns: number;
  readonly tokens: number;
  readonly syntaxNodes: number;
  readonly visits: number;
  readonly maxDepth: number;
}

/**
 * Position-independent per-line layout state. Storing line-relative boundary offsets
 * and per-line resource metrics allows unchanged suffix lines to be reused by reference
 * after incremental convergence without per-line `doc.line()` lookups or allocations.
 */
export interface PrettyLineLayoutState {
  readonly startDepth: number;
  readonly endDepth: number;
  readonly startInBlockComment: boolean;
  readonly endInBlockComment: boolean;
  readonly startInXmlCdata: boolean;
  readonly endInXmlCdata: boolean;
  readonly startXmlRawTag: string | null;
  readonly endXmlRawTag: string | null;
  readonly startQuoteCode: number;
  readonly endQuoteCode: number;
  readonly startLastSigCode: number;
  readonly endLastSigCode: number;
  readonly startDelimSignature: string;
  readonly endDelimSignature: string;
  readonly family: PrettyLayoutFamily;
  readonly eligible: boolean;
  readonly scanStartCol: number;
  readonly relBoundaries: readonly PrettyLineRelativeBoundary[];
  readonly metrics: PrettyLineResourceMetrics;
}

export interface PrettyNativeLayoutOptions {
  readonly sourceIdentity?: string;
  readonly sourceRevision?: number;
  readonly path?: string;
  readonly languageHint?: string;
  readonly indentWidthCh?: number;
  readonly limits?: Partial<PrettyResourceLimits>;
  readonly signal?: AbortSignal;
  readonly shouldCancel?: () => boolean;
}

export interface PrettyNativeLayoutResult {
  readonly sourceIdentity: string;
  readonly sourceRevision: number;
  readonly outcome: PrettyProjectionOutcome;
  readonly mode: DocumentFormatMode;
  readonly family: PrettyLayoutFamily;
  readonly limitReason?: string;
  readonly boundaries: readonly PrettyLayoutBoundary[];
  readonly lineStates: readonly PrettyLineLayoutState[];
  readonly counters: PrettyResourceCounters;
}
