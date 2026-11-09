export type PrettyOriginRunKind =
  | "identity"
  | "virtual-layout"
  | "record-delimiter"
  | "raw-fallback";

export interface PrettyOriginRun {
  readonly kind: PrettyOriginRunKind;
  /** Display UTF-16 interval [dispFrom, dispTo). */
  readonly dispFrom: number;
  readonly dispTo: number;
  /**
   * Source UTF-16 interval [srcFrom, srcTo):
   * - For "identity", "record-delimiter", and "raw-fallback":
   *   dispTo - dispFrom === srcTo - srcFrom (exact 1:1 UTF-16 correspondence).
   * - For "virtual-layout":
   *   [srcFrom, srcTo) is the enclosing insignificant source whitespace interval
   *   between adjacent tokens replaced by this generated layout run.
   */
  readonly srcFrom: number;
  readonly srcTo: number;
  /**
   * Deterministic raw insertion boundary for edits inside a "virtual-layout" run:
   * `srcTo` for leading indentation and post-colon space, or `srcFrom` before closing `}` / `]`.
   */
  readonly insertOffset: number;
  /** Enclosing segment/record index for localized raw-backed fallback. */
  readonly segmentIndex: number;
}

export interface PrettyDisplayLineInfo {
  /** 1-based line number in the raw source snapshot. */
  readonly sourceLine: number;
  /** True when this display line continues the same raw source line as the preceding display line. */
  readonly isContinuation: boolean;
  /** Display UTF-16 start offset of this line. */
  readonly dispFrom: number;
  /** Raw UTF-16 anchor offset associated with this display line. */
  readonly srcAnchor: number;
}

export interface PrettySourceMap {
  readonly sourceRevision: number;
  readonly rawLength: number;
  readonly displayLength: number;
  readonly runs: readonly PrettyOriginRun[];
  /** Packed monotonic display start offsets for O(log N) lookup. */
  readonly dispStarts: Int32Array;
  /** Packed monotonic source start offsets for O(log N) lookup. */
  readonly srcStarts: Int32Array;
  /** Display line number (1-based, indexed at line - 1) metadata. */
  readonly displayLines: readonly PrettyDisplayLineInfo[];
}

export type PrettySegmentKind =
  | "json"
  | "jsonl-record"
  | "envelope-header"
  | "fence-delimiter"
  | "raw";

export interface PrettySegment {
  readonly kind: PrettySegmentKind;
  readonly srcFrom: number;
  readonly srcTo: number;
  readonly dispFrom: number;
  readonly dispTo: number;
  readonly formatted: boolean;
  readonly language?: "json";
}

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
  visits: number;
}

export type PrettyProjectionOutcome =
  | "formatted"
  | "passthrough"
  | "limit-exceeded"
  | "canceled";

export interface PrettyRawRange {
  readonly srcFrom: number;
  readonly srcTo: number;
}

export interface PrettyFormatOptions {
  readonly sourceIdentity: string;
  readonly sourceRevision: number;
  readonly path?: string;
  readonly languageHint?: string;
  readonly indentUnit?: string;
  readonly rawSegmentRanges?: readonly PrettyRawRange[];
  readonly limits?: Partial<PrettyResourceLimits>;
  readonly signal?: AbortSignal;
  readonly shouldCancel?: () => boolean;
}

export interface PrettyProjectionResult {
  readonly sourceIdentity: string;
  readonly sourceRevision: number;
  readonly outcome: PrettyProjectionOutcome;
  readonly limitReason?: string;
  readonly displayText: string;
  readonly sourceMap: PrettySourceMap;
  readonly segments: readonly PrettySegment[];
  readonly counters: PrettyResourceCounters;
}

export interface PrettyUtf16Range {
  readonly from: number;
  readonly to: number;
}

export interface PrettyLineCol {
  /** 1-based line number. */
  readonly line: number;
  /** 1-based UTF-16 column number. */
  readonly col: number;
  /** 0-based UTF-16 offset. */
  readonly offset: number;
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

export interface PrettyProjectedRuneSpan<T extends PrettyInputRuneSpan = PrettyInputRuneSpan> {
  readonly span: T;
  readonly srcFrom: number;
  readonly srcTo: number;
  readonly dispFrom: number;
  readonly dispTo: number;
  readonly from: number;
  readonly to: number;
}

export type PrettyEditIntentKind =
  | "type"
  | "paste"
  | "cut"
  | "delete.backward"
  | "delete.forward"
  | "enter"
  | "tab"
  | "indent"
  | "replace";

export interface PrettyDisplayChange {
  readonly from: number;
  readonly to: number;
  readonly insert: string;
}

export interface PrettySelectionRange {
  readonly anchor: number;
  readonly head: number;
}

export interface PrettyMappedSourceChange {
  readonly from: number;
  readonly to: number;
  readonly insert: string;
}

export interface PrettyRetainedEditIntention {
  readonly order: number;
  readonly dispFrom: number;
  readonly dispTo: number;
  readonly rawFrom: number;
  readonly rawTo: number;
  readonly insert: string;
}

export interface PrettyAmbiguousMultiCursorConflict {
  readonly sourceRevision: number;
  readonly reason: "same-raw-offset-distinct-insert" | "overlapping-raw-ranges";
  readonly intentions: readonly PrettyRetainedEditIntention[];
}

export type PrettyCaretAffinity = "before" | "after";

export interface PrettyEditMapInput {
  readonly map: PrettySourceMap;
  readonly rawText: string;
  readonly displayText: string;
  readonly changes: readonly PrettyDisplayChange[];
  readonly selection?: readonly PrettySelectionRange[];
  readonly intent?: PrettyEditIntentKind;
  readonly composing?: boolean;
  readonly segments?: readonly PrettySegment[];
}

export interface PrettyEditMapResult {
  readonly changes: readonly PrettyMappedSourceChange[];
  readonly rawSelection: readonly PrettySelectionRange[];
  readonly displaySelection: readonly PrettySelectionRange[];
  readonly caretAffinity?: PrettyCaretAffinity;
  readonly movedToRawBoundary: boolean;
  readonly requiresRawFallback: boolean;
  readonly conflictRequiresRawFallback: boolean;
  readonly conflict?: PrettyAmbiguousMultiCursorConflict;
  readonly affectedSegments: readonly number[];
  readonly affectedRawRanges: readonly PrettyRawRange[];
  readonly copiedDisplayText?: string;
  readonly copiedRawText?: string;
}
