import {
  DocumentRecoveryFormatError,
  DocumentStorageError,
} from "../../../files/documents/document-outbox.ts";
import type {
  PrettyAmbiguousMultiCursorConflict,
  PrettyCaretAffinity,
  PrettyDisplayChange,
  PrettyMappedSourceChange,
  PrettyRawRange,
  PrettyRetainedEditIntention,
} from "./pretty-types.ts";

export class PrettyRecoveryCapacityError extends Error {
  constructor(message = "Pretty recovery queue is at capacity; resolve or discard pending recovery items.") {
    super(message);
    this.name = "PrettyRecoveryCapacityError";
  }
}

export interface PrettyPendingConflictIntention extends PrettyRetainedEditIntention {
  readonly baseRuntimeId: string;
  readonly baseIncarnation: string;
  readonly baseEpoch: number;
  readonly baseHistoryRevision: number;
  readonly baseRawFingerprint: string;
}

export interface PrettyPendingConflictRecord {
  readonly conflictId: string;
  readonly documentId: string;
  readonly baseRuntimeId: string;
  readonly baseIncarnation: string;
  readonly baseEpoch: number;
  readonly baseHistoryRevision: number;
  readonly baseRawFingerprint: string;
  readonly reason: "same-raw-offset-distinct-insert" | "overlapping-raw-ranges";
  readonly affectedRawRanges: readonly PrettyRawRange[];
  readonly intentions: PrettyPendingConflictIntention[];
  byteEstimate?: number;
}

export interface PrettyPendingStaleEdit {
  readonly staleEditId: string;
  readonly documentId: string;
  readonly baseRuntimeId: string;
  readonly baseIncarnation: string;
  readonly baseEpoch: number;
  readonly baseHistoryRevision: number;
  readonly baseRawFingerprint: string;
  readonly currentIncarnation: string;
  readonly currentEpoch: number;
  readonly currentHistoryRevision: number;
  readonly reason: "overlap-conflict" | "chain-unavailable";
  readonly intentions: readonly PrettyRetainedEditIntention[];
  readonly rebasedCursorOffset: number;
  byteEstimate?: number;
}

export type PrettyRecoveryOverflowEntry =
  | {
      readonly kind: "conflict";
      readonly record: PrettyPendingConflictRecord;
      readonly bytes: number;
    }
  | {
      readonly kind: "stale-edit";
      readonly record: PrettyPendingStaleEdit;
      readonly bytes: number;
    };

export interface PrettyCaretAffinityState {
  readonly rawOffset: number;
  readonly assoc: PrettyCaretAffinity;
}

export interface PrettyDocumentRecoveryState {
  readonly documentId: string;
  readonly incarnation: string;
  pendingConflicts: PrettyPendingConflictRecord[];
  pendingStaleEdits: PrettyPendingStaleEdit[];
  rawSegmentRanges: PrettyRawRange[];
  retainedBytes: number;
  capacityOverflowRecord: PrettyRecoveryOverflowEntry | null;
  preservationBlocked: boolean;
  storageReadBlocked: boolean;
  storageFormatBlocked: boolean;
  unreadableRawPayload: string | null;
  lastStorageError: DocumentStorageError | null;
}

export interface PrettyRecoveryStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export interface PrettyRecoveryLimits {
  readonly maxRecords: number;
  readonly maxRetainedBytes: number;
}

export type PrettyRecoveryPersistResult =
  | { readonly ok: true }
  | {
      readonly ok: false;
      readonly reason:
        | "storage-unavailable"
        | "storage-read-blocked"
        | "storage-format-blocked"
        | "storage-write-failed";
      readonly error: DocumentStorageError;
    };

export type PrettyRecoveryAppendResult<T> =
  | {
      readonly status: "admitted";
      readonly record: T;
      readonly persisted: PrettyRecoveryPersistResult;
    }
  | {
      readonly status: "held-in-overflow";
      readonly record: T;
    }
  | {
      readonly status: "refused-at-capacity";
      readonly error: PrettyRecoveryCapacityError;
    };

export const PRETTY_RECOVERY_STORAGE_PREFIX = "painted-wolf:pretty-recovery:v1:";
export const DEFAULT_PRETTY_MAX_RECOVERY_RECORDS = 64;
export const DEFAULT_PRETTY_MAX_RECOVERY_BYTES = 256 * 1024;

let nextRecoveryRecordSequence = 0;
let nextHistoryRuntimeSequence = 0;
const historyRuntimeIds = new WeakMap<object, string>();
const utf8Encoder = new TextEncoder();

let activeLimits: PrettyRecoveryLimits = {
  maxRecords: DEFAULT_PRETTY_MAX_RECOVERY_RECORDS,
  maxRetainedBytes: DEFAULT_PRETTY_MAX_RECOVERY_BYTES,
};

export function configurePrettyRecoveryLimitsForTests(
  limits?: Partial<PrettyRecoveryLimits>,
): void {
  activeLimits = {
    maxRecords: limits?.maxRecords ?? DEFAULT_PRETTY_MAX_RECOVERY_RECORDS,
    maxRetainedBytes: limits?.maxRetainedBytes ?? DEFAULT_PRETTY_MAX_RECOVERY_BYTES,
  };
}

export function getPrettyRecoveryLimits(): PrettyRecoveryLimits {
  return activeLimits;
}

export function getHistoryRuntimeId(historyInstance: object): string {
  let id = historyRuntimeIds.get(historyInstance);
  if (!id) {
    id = `runtime-${++nextHistoryRuntimeSequence}`;
    historyRuntimeIds.set(historyInstance, id);
  }
  return id;
}

export function computeRawTextFingerprint(rawText: string): string {
  let h1 = 0x811c9dc5;
  let h2 = 5381;
  for (let i = 0; i < rawText.length; i++) {
    const code = rawText.charCodeAt(i);
    h1 ^= code & 0xff;
    h1 = Math.imul(h1, 0x01000193) >>> 0;
    h1 ^= (code >>> 8) & 0xff;
    h1 = Math.imul(h1, 0x01000193) >>> 0;
    h2 = (Math.imul(h2, 33) ^ code) >>> 0;
  }
  return `len:${rawText.length}:h1:${h1.toString(16).padStart(8, "0")}:h2:${h2.toString(16).padStart(8, "0")}`;
}

export function estimateRecoveryRecordBytes(
  record: PrettyPendingConflictRecord | PrettyPendingStaleEdit,
): number {
  const cached = record.byteEstimate;
  if (typeof cached === "number" && cached > 0) return cached;
  const measured = utf8Encoder.encode(JSON.stringify(record)).byteLength;
  record.byteEstimate = measured;
  return measured;
}

export function recomputeRecoveryRetainedBytes(state: PrettyDocumentRecoveryState): number {
  let total = 0;
  for (const c of state.pendingConflicts) {
    c.byteEstimate = undefined;
    total += estimateRecoveryRecordBytes(c);
  }
  for (const s of state.pendingStaleEdits) {
    s.byteEstimate = undefined;
    total += estimateRecoveryRecordBytes(s);
  }
  state.retainedBytes = total;
  return total;
}

function recoveryMapKey(documentId: string, incarnation: string): string {
  return `${documentId}::${incarnation}`;
}

export function prettyRecoveryStorageKey(documentId: string, incarnation: string): string {
  return `${PRETTY_RECOVERY_STORAGE_PREFIX}${encodeURIComponent(documentId)}:${encodeURIComponent(incarnation)}`;
}

function defaultBrowserStorage(): PrettyRecoveryStorage | null {
  try {
    if (typeof globalThis.localStorage !== "undefined" && globalThis.localStorage !== null) {
      return globalThis.localStorage;
    }
  } catch {
    // localStorage may be unavailable in restricted environments.
  }
  return null;
}

let customRecoveryStorage: PrettyRecoveryStorage | null | undefined;
const trackedStorageKeys = new Set<string>();
const recoveryByKey = new Map<string, PrettyDocumentRecoveryState>();

function activeRecoveryStorage(): PrettyRecoveryStorage | null {
  if (customRecoveryStorage !== undefined) return customRecoveryStorage;
  return defaultBrowserStorage();
}

export function configurePrettyRecoveryStorageForTests(
  storage?: PrettyRecoveryStorage | null,
): void {
  customRecoveryStorage = storage;
}

function isValidRawRange(value: unknown): value is PrettyRawRange {
  if (!value || typeof value !== "object") return false;
  const r = value as Partial<PrettyRawRange>;
  return (
    typeof r.srcFrom === "number" &&
    Number.isFinite(r.srcFrom) &&
    typeof r.srcTo === "number" &&
    Number.isFinite(r.srcTo) &&
    r.srcFrom >= 0 &&
    r.srcTo >= r.srcFrom
  );
}

function isValidRetainedIntention(value: unknown): value is PrettyRetainedEditIntention {
  if (!value || typeof value !== "object") return false;
  const item = value as Partial<PrettyRetainedEditIntention>;
  return (
    typeof item.order === "number" &&
    Number.isFinite(item.order) &&
    typeof item.dispFrom === "number" &&
    typeof item.dispTo === "number" &&
    typeof item.rawFrom === "number" &&
    typeof item.rawTo === "number" &&
    typeof item.insert === "string"
  );
}

function isValidConflictIntention(value: unknown): value is PrettyPendingConflictIntention {
  if (!isValidRetainedIntention(value)) return false;
  const item = value as Partial<PrettyPendingConflictIntention>;
  return (
    typeof item.baseRuntimeId === "string" &&
    item.baseRuntimeId.length > 0 &&
    typeof item.baseIncarnation === "string" &&
    item.baseIncarnation.length > 0 &&
    typeof item.baseEpoch === "number" &&
    Number.isFinite(item.baseEpoch) &&
    typeof item.baseHistoryRevision === "number" &&
    Number.isFinite(item.baseHistoryRevision) &&
    typeof item.baseRawFingerprint === "string" &&
    item.baseRawFingerprint.length > 0
  );
}

function createEmptyRecoveryState(
  documentId: string,
  incarnation: string,
): PrettyDocumentRecoveryState {
  return {
    documentId,
    incarnation,
    pendingConflicts: [],
    pendingStaleEdits: [],
    rawSegmentRanges: [],
    retainedBytes: 0,
    capacityOverflowRecord: null,
    preservationBlocked: false,
    storageReadBlocked: false,
    storageFormatBlocked: false,
    unreadableRawPayload: null,
    lastStorageError: null,
  };
}

function parsePersistedRecoveryState(
  raw: string,
  expectedDocumentId: string,
  expectedIncarnation: string,
): PrettyDocumentRecoveryState | null {
  try {
    const parsed = JSON.parse(raw) as Partial<PrettyDocumentRecoveryState> & { version?: number };
    if (
      !parsed ||
      typeof parsed !== "object" ||
      parsed.version !== 1 ||
      parsed.documentId !== expectedDocumentId ||
      parsed.incarnation !== expectedIncarnation ||
      !Array.isArray(parsed.pendingConflicts) ||
      !Array.isArray(parsed.pendingStaleEdits) ||
      !Array.isArray(parsed.rawSegmentRanges) ||
      !parsed.rawSegmentRanges.every(isValidRawRange)
    ) {
      return null;
    }

    const pendingConflicts: PrettyPendingConflictRecord[] = [];
    for (const c of parsed.pendingConflicts) {
      if (
        !c ||
        typeof c !== "object" ||
        typeof c.conflictId !== "string" ||
        c.documentId !== expectedDocumentId ||
        typeof c.baseRuntimeId !== "string" ||
        typeof c.baseIncarnation !== "string" ||
        typeof c.baseEpoch !== "number" ||
        typeof c.baseHistoryRevision !== "number" ||
        typeof c.baseRawFingerprint !== "string" ||
        (c.reason !== "same-raw-offset-distinct-insert" &&
          c.reason !== "overlapping-raw-ranges") ||
        !Array.isArray(c.affectedRawRanges) ||
        !c.affectedRawRanges.every(isValidRawRange) ||
        !Array.isArray(c.intentions) ||
        !c.intentions.every(isValidConflictIntention)
      ) {
        return null;
      }
      pendingConflicts.push({
        conflictId: c.conflictId,
        documentId: c.documentId,
        baseRuntimeId: c.baseRuntimeId,
        baseIncarnation: c.baseIncarnation,
        baseEpoch: c.baseEpoch,
        baseHistoryRevision: c.baseHistoryRevision,
        baseRawFingerprint: c.baseRawFingerprint,
        reason: c.reason,
        affectedRawRanges: c.affectedRawRanges.map((r) => ({ srcFrom: r.srcFrom, srcTo: r.srcTo })),
        intentions: c.intentions.map((i) => ({ ...i })),
      });
    }

    const pendingStaleEdits: PrettyPendingStaleEdit[] = [];
    for (const s of parsed.pendingStaleEdits) {
      if (
        !s ||
        typeof s !== "object" ||
        typeof s.staleEditId !== "string" ||
        s.documentId !== expectedDocumentId ||
        typeof s.baseRuntimeId !== "string" ||
        typeof s.baseIncarnation !== "string" ||
        typeof s.baseEpoch !== "number" ||
        typeof s.baseHistoryRevision !== "number" ||
        typeof s.baseRawFingerprint !== "string" ||
        typeof s.currentIncarnation !== "string" ||
        typeof s.currentEpoch !== "number" ||
        typeof s.currentHistoryRevision !== "number" ||
        (s.reason !== "overlap-conflict" && s.reason !== "chain-unavailable") ||
        typeof s.rebasedCursorOffset !== "number" ||
        !Array.isArray(s.intentions) ||
        !s.intentions.every(isValidRetainedIntention)
      ) {
        return null;
      }
      pendingStaleEdits.push({
        staleEditId: s.staleEditId,
        documentId: s.documentId,
        baseRuntimeId: s.baseRuntimeId,
        baseIncarnation: s.baseIncarnation,
        baseEpoch: s.baseEpoch,
        baseHistoryRevision: s.baseHistoryRevision,
        baseRawFingerprint: s.baseRawFingerprint,
        currentIncarnation: s.currentIncarnation,
        currentEpoch: s.currentEpoch,
        currentHistoryRevision: s.currentHistoryRevision,
        reason: s.reason,
        intentions: s.intentions.map((i) => ({ ...i })),
        rebasedCursorOffset: s.rebasedCursorOffset,
      });
    }

    const state: PrettyDocumentRecoveryState = {
      documentId: expectedDocumentId,
      incarnation: expectedIncarnation,
      pendingConflicts,
      pendingStaleEdits,
      rawSegmentRanges: parsed.rawSegmentRanges.map((r) => ({
        srcFrom: r.srcFrom,
        srcTo: r.srcTo,
      })),
      retainedBytes: 0,
      capacityOverflowRecord: null,
      preservationBlocked: false,
      storageReadBlocked: false,
      storageFormatBlocked: false,
      unreadableRawPayload: null,
      lastStorageError: null,
    };
    recomputeRecoveryRetainedBytes(state);
    return state;
  } catch {
    return null;
  }
}

export function getPrettyDocumentRecoveryState(
  documentId: string,
  incarnation: string,
): PrettyDocumentRecoveryState {
  const key = recoveryMapKey(documentId, incarnation);
  const existing = recoveryByKey.get(key);
  if (existing) return existing;

  const storage = activeRecoveryStorage();
  const storageKey = prettyRecoveryStorageKey(documentId, incarnation);
  if (!storage) {
    const state = createEmptyRecoveryState(documentId, incarnation);
    recoveryByKey.set(key, state);
    return state;
  }

  try {
    const raw = storage.getItem(storageKey);
    if (raw !== null) {
      trackedStorageKeys.add(storageKey);
      const loaded = parsePersistedRecoveryState(raw, documentId, incarnation);
      if (loaded) {
        recoveryByKey.set(key, loaded);
        return loaded;
      }
      const formatErr = new DocumentStorageError(
        new DocumentRecoveryFormatError(
          "This Pretty recovery store has an unknown or invalid format. Its preserved bytes have been left untouched.",
        ),
      );
      const blockedState = createEmptyRecoveryState(documentId, incarnation);
      blockedState.storageFormatBlocked = true;
      blockedState.preservationBlocked = true;
      blockedState.unreadableRawPayload = raw;
      blockedState.lastStorageError = formatErr;
      recoveryByKey.set(key, blockedState);
      return blockedState;
    }
  } catch (cause) {
    const readErr = new DocumentStorageError(cause);
    const blockedState = createEmptyRecoveryState(documentId, incarnation);
    blockedState.storageReadBlocked = true;
    blockedState.preservationBlocked = true;
    blockedState.lastStorageError = readErr;
    recoveryByKey.set(key, blockedState);
    return blockedState;
  }

  const state = createEmptyRecoveryState(documentId, incarnation);
  recoveryByKey.set(key, state);
  return state;
}

export function persistPrettyDocumentRecoveryState(
  state: PrettyDocumentRecoveryState,
): PrettyRecoveryPersistResult {
  if (state.storageFormatBlocked || state.unreadableRawPayload !== null) {
    const err =
      state.lastStorageError ??
      new DocumentStorageError(
        new DocumentRecoveryFormatError(
          "This Pretty recovery store has an unknown or invalid format. Its preserved bytes have been left untouched.",
        ),
      );
    state.preservationBlocked = true;
    state.lastStorageError = err;
    return { ok: false, reason: "storage-format-blocked", error: err };
  }
  if (state.storageReadBlocked) {
    const err =
      state.lastStorageError ??
      new DocumentStorageError(new Error("Pretty recovery storage could not be read; refusing to overwrite unread recovery state."));
    state.preservationBlocked = true;
    state.lastStorageError = err;
    return { ok: false, reason: "storage-read-blocked", error: err };
  }

  const storage = activeRecoveryStorage();
  const storageKey = prettyRecoveryStorageKey(state.documentId, state.incarnation);
  const isEmpty =
    state.pendingConflicts.length === 0 &&
    state.pendingStaleEdits.length === 0 &&
    state.rawSegmentRanges.length === 0;

  if (!storage) {
    const err = new DocumentStorageError(new Error("Pretty recovery storage is unavailable."));
    state.preservationBlocked = true;
    state.lastStorageError = err;
    return { ok: false, reason: "storage-unavailable", error: err };
  }

  try {
    if (isEmpty) {
      storage.removeItem(storageKey);
      trackedStorageKeys.delete(storageKey);
    } else {
      const payload = JSON.stringify({
        version: 1,
        documentId: state.documentId,
        incarnation: state.incarnation,
        pendingConflicts: state.pendingConflicts,
        pendingStaleEdits: state.pendingStaleEdits,
        rawSegmentRanges: state.rawSegmentRanges,
      });
      storage.setItem(storageKey, payload);
      trackedStorageKeys.add(storageKey);
    }
    state.preservationBlocked = state.capacityOverflowRecord !== null;
    state.lastStorageError = null;
    return { ok: true };
  } catch (cause) {
    const err = new DocumentStorageError(cause);
    state.preservationBlocked = true;
    state.lastStorageError = err;
    return { ok: false, reason: "storage-write-failed", error: err };
  }
}

export function recomputeRecoveryRawSegmentRanges(
  state: PrettyDocumentRecoveryState,
  extraRanges: readonly PrettyRawRange[] = [],
): void {
  const ranges: PrettyRawRange[] = [];
  for (const conflict of state.pendingConflicts) {
    for (const r of conflict.affectedRawRanges) {
      ranges.push({ srcFrom: r.srcFrom, srcTo: r.srcTo });
    }
  }
  for (const stale of state.pendingStaleEdits) {
    ranges.push({ srcFrom: stale.rebasedCursorOffset, srcTo: stale.rebasedCursorOffset });
  }
  if (state.capacityOverflowRecord) {
    if (state.capacityOverflowRecord.kind === "conflict") {
      for (const r of state.capacityOverflowRecord.record.affectedRawRanges) {
        ranges.push({ srcFrom: r.srcFrom, srcTo: r.srcTo });
      }
    } else {
      const off = state.capacityOverflowRecord.record.rebasedCursorOffset;
      ranges.push({ srcFrom: off, srcTo: off });
    }
  }
  for (const r of extraRanges) {
    ranges.push({ srcFrom: r.srcFrom, srcTo: r.srcTo });
  }
  state.rawSegmentRanges = ranges;
}

export function promoteOverflowRecordIfPossible(
  recovery: PrettyDocumentRecoveryState,
): boolean {
  const overflow = recovery.capacityOverflowRecord;
  if (!overflow) return false;
  const totalCount = recovery.pendingConflicts.length + recovery.pendingStaleEdits.length;
  if (
    totalCount >= activeLimits.maxRecords ||
    recovery.retainedBytes + overflow.bytes > activeLimits.maxRetainedBytes
  ) {
    return false;
  }
  recovery.capacityOverflowRecord = null;
  if (overflow.kind === "conflict") {
    recovery.pendingConflicts.push(overflow.record);
  } else {
    recovery.pendingStaleEdits.push(overflow.record);
  }
  recovery.retainedBytes += overflow.bytes;
  return true;
}

export function appendMultiCursorConflictToRecovery(
  recovery: PrettyDocumentRecoveryState,
  baseRuntimeId: string,
  baseIncarnation: string,
  baseEpoch: number,
  baseHistoryRevision: number,
  baseRawFingerprint: string,
  conflict: PrettyAmbiguousMultiCursorConflict,
  affectedRawRanges: readonly PrettyRawRange[],
): PrettyRecoveryAppendResult<PrettyPendingConflictRecord> {
  if (recovery.capacityOverflowRecord !== null) {
    return {
      status: "refused-at-capacity",
      error: new PrettyRecoveryCapacityError(),
    };
  }
  const record: PrettyPendingConflictRecord = {
    conflictId: `conflict-${recovery.documentId}-${recovery.incarnation}-${baseHistoryRevision}-${++nextRecoveryRecordSequence}`,
    documentId: recovery.documentId,
    baseRuntimeId,
    baseIncarnation,
    baseEpoch,
    baseHistoryRevision,
    baseRawFingerprint,
    reason: conflict.reason,
    affectedRawRanges: affectedRawRanges.map((r) => ({ srcFrom: r.srcFrom, srcTo: r.srcTo })),
    intentions: conflict.intentions.map((item) => ({
      ...item,
      baseRuntimeId,
      baseIncarnation,
      baseEpoch,
      baseHistoryRevision,
      baseRawFingerprint,
    })),
  };
  const recordBytes = estimateRecoveryRecordBytes(record);
  const totalCount = recovery.pendingConflicts.length + recovery.pendingStaleEdits.length;
  if (
    totalCount >= activeLimits.maxRecords ||
    recovery.retainedBytes + recordBytes > activeLimits.maxRetainedBytes
  ) {
    recovery.capacityOverflowRecord = {
      kind: "conflict",
      record,
      bytes: recordBytes,
    };
    recovery.preservationBlocked = true;
    recomputeRecoveryRawSegmentRanges(recovery);
    return { status: "held-in-overflow", record };
  }

  recovery.pendingConflicts.push(record);
  recovery.retainedBytes += recordBytes;
  recomputeRecoveryRawSegmentRanges(recovery);
  const persisted = persistPrettyDocumentRecoveryState(recovery);
  return { status: "admitted", record, persisted };
}

export function appendStaleEditToRecovery(
  recovery: PrettyDocumentRecoveryState,
  baseRuntimeId: string,
  baseIncarnation: string,
  baseEpoch: number,
  baseHistoryRevision: number,
  baseRawFingerprint: string,
  currentIncarnation: string,
  currentEpoch: number,
  currentHistoryRevision: number,
  reason: "overlap-conflict" | "chain-unavailable",
  displayChanges: readonly PrettyDisplayChange[],
  mappedChanges: readonly PrettyMappedSourceChange[],
  rebasedCursorOffset: number,
): PrettyRecoveryAppendResult<PrettyPendingStaleEdit> {
  if (recovery.capacityOverflowRecord !== null) {
    return {
      status: "refused-at-capacity",
      error: new PrettyRecoveryCapacityError(),
    };
  }
  const staleRecord: PrettyPendingStaleEdit = {
    staleEditId: `stale-${recovery.documentId}-${recovery.incarnation}-${baseHistoryRevision}-${++nextRecoveryRecordSequence}`,
    documentId: recovery.documentId,
    baseRuntimeId,
    baseIncarnation,
    baseEpoch,
    baseHistoryRevision,
    baseRawFingerprint,
    currentIncarnation,
    currentEpoch,
    currentHistoryRevision,
    reason,
    intentions: mappedChanges.map((c, idx) => ({
      order: idx,
      dispFrom: displayChanges[idx]?.from ?? 0,
      dispTo: displayChanges[idx]?.to ?? 0,
      rawFrom: c.from,
      rawTo: c.to,
      insert: c.insert,
    })),
    rebasedCursorOffset,
  };
  const recordBytes = estimateRecoveryRecordBytes(staleRecord);
  const totalCount = recovery.pendingConflicts.length + recovery.pendingStaleEdits.length;
  if (
    totalCount >= activeLimits.maxRecords ||
    recovery.retainedBytes + recordBytes > activeLimits.maxRetainedBytes
  ) {
    recovery.capacityOverflowRecord = {
      kind: "stale-edit",
      record: staleRecord,
      bytes: recordBytes,
    };
    recovery.preservationBlocked = true;
    recomputeRecoveryRawSegmentRanges(recovery);
    return { status: "held-in-overflow", record: staleRecord };
  }

  recovery.pendingStaleEdits.push(staleRecord);
  recovery.retainedBytes += recordBytes;
  recomputeRecoveryRawSegmentRanges(recovery);
  const persisted = persistPrettyDocumentRecoveryState(recovery);
  return { status: "admitted", record: staleRecord, persisted };
}

export function simulatePrettyProcessRestartForTests(): void {
  recoveryByKey.clear();
}

export function resetPrettyDocumentRecoveryStoreForTests(): void {
  recoveryByKey.clear();
  configurePrettyRecoveryLimitsForTests();
  const storage = activeRecoveryStorage();
  if (storage) {
    for (const key of trackedStorageKeys) {
      try {
        storage.removeItem(key);
      } catch {
        // Ignore storage cleanup errors in test reset.
      }
    }
  }
  trackedStorageKeys.clear();
  customRecoveryStorage = undefined;
}
