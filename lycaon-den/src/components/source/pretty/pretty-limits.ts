import type {
  PrettyProjectionResult,
  PrettyResourceCounters,
  PrettyResourceLimits,
} from "./pretty-types.ts";

export const DEFAULT_PRETTY_RESOURCE_LIMITS: PrettyResourceLimits = {
  maxInputUtf8Bytes: 2_000_000,
  maxInputUtf16Units: 2_000_000,
  maxOutputUtf8Bytes: 4_000_000,
  maxOutputUtf16Units: 4_000_000,
  maxOutputLines: 50_000,
  maxNestingDepth: 128,
  maxTokens: 500_000,
  maxMappingRuns: 500_000,
  maxCacheBytes: 8 * 1024 * 1024,
};

/** Aggregate scanner/printer visit limit: 32 * inputUtf16Length + 1024. */
export function maxPrettyVisits(inputUtf16Length: number): number {
  return 32 * Math.max(0, inputUtf16Length) + 1024;
}

export function resolvePrettyLimits(
  overrides?: Partial<PrettyResourceLimits>,
): PrettyResourceLimits {
  if (!overrides) return DEFAULT_PRETTY_RESOURCE_LIMITS;
  return {
    ...DEFAULT_PRETTY_RESOURCE_LIMITS,
    ...overrides,
  };
}

/** Deterministic UTF-8 byte count for a string slice without allocating a Uint8Array. */
export function utf8ByteLengthSlice(
  text: string,
  from = 0,
  to = text.length,
): number {
  let bytes = 0;
  for (let i = from; i < to; i++) {
    const code = text.charCodeAt(i);
    if (code < 0x80) {
      bytes += 1;
    } else if (code < 0x800) {
      bytes += 2;
    } else if (code >= 0xd800 && code <= 0xdbff && i + 1 < to) {
      const next = text.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        bytes += 4;
        i += 1;
      } else {
        bytes += 3;
      }
    } else {
      bytes += 3;
    }
  }
  return bytes;
}

export function utf8ByteLength(text: string): number {
  return utf8ByteLengthSlice(text, 0, text.length);
}

export function countNewlinesSlice(
  text: string,
  from = 0,
  to = text.length,
): number {
  let count = 0;
  for (let i = from; i < to; i++) {
    if (text.charCodeAt(i) === 0x0a) count += 1;
  }
  return count;
}

export function createResourceCounters(
  inputUtf8Bytes: number,
  inputUtf16Units: number,
): PrettyResourceCounters {
  return {
    inputUtf8Bytes,
    inputUtf16Units,
    outputUtf8Bytes: 0,
    outputUtf16Units: 0,
    outputLines: 1,
    maxDepthReached: 0,
    tokens: 0,
    mappingRuns: 0,
    visits: 0,
  };
}

export class PrettyBudgetTracker {
  readonly limits: PrettyResourceLimits;
  readonly visitCeiling: number;
  readonly counters: PrettyResourceCounters;
  limitReason: string | undefined;
  canceled = false;
  private readonly signal: AbortSignal | undefined;
  private readonly shouldCancel: (() => boolean) | undefined;

  constructor(
    rawText: string,
    limits?: Partial<PrettyResourceLimits>,
    signal?: AbortSignal,
    shouldCancel?: () => boolean,
  ) {
    this.limits = resolvePrettyLimits(limits);
    this.visitCeiling =
      this.limits.maxVisits ?? maxPrettyVisits(rawText.length);
    this.signal = signal;
    this.shouldCancel = shouldCancel;
    const inputUtf16Units = rawText.length;
    const inputUtf8Bytes =
      inputUtf16Units <= this.limits.maxInputUtf16Units
        ? utf8ByteLength(rawText)
        : inputUtf16Units;
    this.counters = createResourceCounters(inputUtf8Bytes, inputUtf16Units);
  }

  checkCancellation(): boolean {
    if (this.canceled) return false;
    if (this.signal?.aborted || this.shouldCancel?.() === true) {
      this.canceled = true;
      return false;
    }
    return true;
  }

  checkInput(): boolean {
    if (!this.checkCancellation()) return false;
    if (this.counters.inputUtf16Units > this.limits.maxInputUtf16Units) {
      this.limitReason = "maxInputUtf16Units";
      return false;
    }
    if (this.counters.inputUtf8Bytes > this.limits.maxInputUtf8Bytes) {
      this.limitReason = "maxInputUtf8Bytes";
      return false;
    }
    if (this.limits.maxOutputLines < 1) {
      this.limitReason = "maxOutputLines";
      return false;
    }
    return true;
  }

  recordVisit(steps = 1): boolean {
    if (this.canceled || this.limitReason) return false;
    const next = this.counters.visits + steps;
    if ((this.counters.visits & 63) === 0 && !this.checkCancellation()) {
      return false;
    }
    if (next > this.visitCeiling) {
      this.limitReason = "maxVisits";
      return false;
    }
    this.counters.visits = next;
    return true;
  }

  recordDepth(depth: number): boolean {
    if (depth > this.limits.maxNestingDepth) {
      this.limitReason = "maxNestingDepth";
      return false;
    }
    if (depth > this.counters.maxDepthReached) {
      this.counters.maxDepthReached = depth;
    }
    return true;
  }

  recordToken(): boolean {
    if (!this.checkCancellation()) return false;
    if (this.counters.tokens + 1 > this.limits.maxTokens) {
      this.limitReason = "maxTokens";
      return false;
    }
    this.counters.tokens += 1;
    return true;
  }

  recordOutputRun(
    utf16Delta: number,
    utf8Delta: number,
    newlineDelta: number,
  ): boolean {
    if (!this.checkCancellation()) return false;
    if (this.counters.mappingRuns + 1 > this.limits.maxMappingRuns) {
      this.limitReason = "maxMappingRuns";
      return false;
    }
    if (
      this.counters.outputUtf16Units + utf16Delta >
      this.limits.maxOutputUtf16Units
    ) {
      this.limitReason = "maxOutputUtf16Units";
      return false;
    }
    if (
      this.counters.outputUtf8Bytes + utf8Delta >
      this.limits.maxOutputUtf8Bytes
    ) {
      this.limitReason = "maxOutputUtf8Bytes";
      return false;
    }
    if (
      this.counters.outputLines + newlineDelta >
      this.limits.maxOutputLines
    ) {
      this.limitReason = "maxOutputLines";
      return false;
    }
    this.counters.mappingRuns += 1;
    this.counters.outputUtf16Units += utf16Delta;
    this.counters.outputUtf8Bytes += utf8Delta;
    this.counters.outputLines += newlineDelta;
    return true;
  }
}

/** Accounts for display text, origin runs, index arrays, line table, and segments. */
export function estimateProjectionCacheBytes(
  result: PrettyProjectionResult,
): number {
  const textBytes = result.displayText.length * 2;
  const runsBytes = result.sourceMap.runs.length * 48;
  const startsBytes =
    result.sourceMap.dispStarts.byteLength +
    result.sourceMap.srcStarts.byteLength;
  const linesBytes = result.sourceMap.displayLines.length * 24;
  const segmentsBytes = result.segments.length * 40;
  return 256 + textBytes + runsBytes + startsBytes + linesBytes + segmentsBytes;
}
