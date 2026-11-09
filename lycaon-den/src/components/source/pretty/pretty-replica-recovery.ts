import type {
  PrettyAmbiguousMultiCursorConflict,
  PrettyCaretAffinity,
  PrettyDisplayChange,
  PrettyMappedSourceChange,
  PrettyRawRange,
  PrettyRetainedEditIntention,
} from "./pretty-types.ts";

export interface PrettyPendingConflictIntention extends PrettyRetainedEditIntention {
  readonly baseIncarnation: string;
  readonly baseEpoch: number;
  readonly baseHistoryRevision: number;
}

export interface PrettyPendingConflictRecord {
  readonly conflictId: string;
  readonly documentId: string;
  readonly baseIncarnation: string;
  readonly baseEpoch: number;
  readonly baseHistoryRevision: number;
  readonly reason: "same-raw-offset-distinct-insert" | "overlapping-raw-ranges";
  readonly affectedRawRanges: readonly PrettyRawRange[];
  readonly intentions: PrettyPendingConflictIntention[];
}

export interface PrettyPendingStaleEdit {
  readonly staleEditId: string;
  readonly documentId: string;
  readonly baseIncarnation: string;
  readonly baseEpoch: number;
  readonly baseHistoryRevision: number;
  readonly currentIncarnation: string;
  readonly currentEpoch: number;
  readonly currentHistoryRevision: number;
  readonly reason: "overlap-conflict" | "chain-unavailable";
  readonly intentions: readonly PrettyRetainedEditIntention[];
  readonly rebasedCursorOffset: number;
}

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
}

export interface PrettyRecoveryStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

const PRETTY_RECOVERY_STORAGE_PREFIX = "painted-wolf:pretty-recovery:v1:";
let nextRecoveryRecordSequence = 0;

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
    typeof item.baseIncarnation === "string" &&
    item.baseIncarnation.length > 0 &&
    typeof item.baseEpoch === "number" &&
    Number.isFinite(item.baseEpoch) &&
    typeof item.baseHistoryRevision === "number" &&
    Number.isFinite(item.baseHistoryRevision)
  );
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
        typeof c.baseIncarnation !== "string" ||
        typeof c.baseEpoch !== "number" ||
        typeof c.baseHistoryRevision !== "number" ||
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
        baseIncarnation: c.baseIncarnation,
        baseEpoch: c.baseEpoch,
        baseHistoryRevision: c.baseHistoryRevision,
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
        typeof s.baseIncarnation !== "string" ||
        typeof s.baseEpoch !== "number" ||
        typeof s.baseHistoryRevision !== "number" ||
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
        baseIncarnation: s.baseIncarnation,
        baseEpoch: s.baseEpoch,
        baseHistoryRevision: s.baseHistoryRevision,
        currentIncarnation: s.currentIncarnation,
        currentEpoch: s.currentEpoch,
        currentHistoryRevision: s.currentHistoryRevision,
        reason: s.reason,
        intentions: s.intentions.map((i) => ({ ...i })),
        rebasedCursorOffset: s.rebasedCursorOffset,
      });
    }

    return {
      documentId: expectedDocumentId,
      incarnation: expectedIncarnation,
      pendingConflicts,
      pendingStaleEdits,
      rawSegmentRanges: parsed.rawSegmentRanges.map((r) => ({
        srcFrom: r.srcFrom,
        srcTo: r.srcTo,
      })),
    };
  } catch {
    return null;
  }
}

export function getPrettyDocumentRecoveryState(
  documentId: string,
  incarnation: string,
): PrettyDocumentRecoveryState {
  const key = recoveryMapKey(documentId, incarnation);
  let state = recoveryByKey.get(key);
  if (state) return state;

  const storage = activeRecoveryStorage();
  const storageKey = prettyRecoveryStorageKey(documentId, incarnation);
  if (storage) {
    try {
      const raw = storage.getItem(storageKey);
      if (raw !== null) {
        const loaded = parsePersistedRecoveryState(raw, documentId, incarnation);
        if (loaded) {
          recoveryByKey.set(key, loaded);
          trackedStorageKeys.add(storageKey);
          return loaded;
        }
      }
    } catch {
      // Fall back to empty in-memory recovery record if storage read fails.
    }
  }

  state = {
    documentId,
    incarnation,
    pendingConflicts: [],
    pendingStaleEdits: [],
    rawSegmentRanges: [],
  };
  recoveryByKey.set(key, state);
  return state;
}

export function persistPrettyDocumentRecoveryState(state: PrettyDocumentRecoveryState): void {
  const storage = activeRecoveryStorage();
  if (!storage) return;
  const storageKey = prettyRecoveryStorageKey(state.documentId, state.incarnation);
  const isEmpty =
    state.pendingConflicts.length === 0 &&
    state.pendingStaleEdits.length === 0 &&
    state.rawSegmentRanges.length === 0;
  if (isEmpty) {
    storage.removeItem(storageKey);
    trackedStorageKeys.delete(storageKey);
    return;
  }
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
  for (const r of extraRanges) {
    ranges.push({ srcFrom: r.srcFrom, srcTo: r.srcTo });
  }
  state.rawSegmentRanges = ranges;
}

export function appendMultiCursorConflictToRecovery(
  recovery: PrettyDocumentRecoveryState,
  baseIncarnation: string,
  baseEpoch: number,
  baseHistoryRevision: number,
  conflict: PrettyAmbiguousMultiCursorConflict,
  affectedRawRanges: readonly PrettyRawRange[],
): PrettyPendingConflictRecord {
  const record: PrettyPendingConflictRecord = {
    conflictId: `conflict-${recovery.documentId}-${recovery.incarnation}-${baseHistoryRevision}-${++nextRecoveryRecordSequence}`,
    documentId: recovery.documentId,
    baseIncarnation,
    baseEpoch,
    baseHistoryRevision,
    reason: conflict.reason,
    affectedRawRanges: affectedRawRanges.map((r) => ({ srcFrom: r.srcFrom, srcTo: r.srcTo })),
    intentions: conflict.intentions.map((item) => ({
      ...item,
      baseIncarnation,
      baseEpoch,
      baseHistoryRevision,
    })),
  };
  recovery.pendingConflicts.push(record);
  recomputeRecoveryRawSegmentRanges(recovery);
  persistPrettyDocumentRecoveryState(recovery);
  return record;
}

export function appendStaleEditToRecovery(
  recovery: PrettyDocumentRecoveryState,
  baseIncarnation: string,
  baseEpoch: number,
  baseHistoryRevision: number,
  currentIncarnation: string,
  currentEpoch: number,
  currentHistoryRevision: number,
  reason: "overlap-conflict" | "chain-unavailable",
  displayChanges: readonly PrettyDisplayChange[],
  mappedChanges: readonly PrettyMappedSourceChange[],
  rebasedCursorOffset: number,
): PrettyPendingStaleEdit {
  const staleRecord: PrettyPendingStaleEdit = {
    staleEditId: `stale-${recovery.documentId}-${recovery.incarnation}-${baseHistoryRevision}-${++nextRecoveryRecordSequence}`,
    documentId: recovery.documentId,
    baseIncarnation,
    baseEpoch,
    baseHistoryRevision,
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
  recovery.pendingStaleEdits.push(staleRecord);
  recomputeRecoveryRawSegmentRanges(recovery);
  persistPrettyDocumentRecoveryState(recovery);
  return staleRecord;
}

export function simulatePrettyProcessRestartForTests(): void {
  recoveryByKey.clear();
}

export function resetPrettyDocumentRecoveryStoreForTests(): void {
  recoveryByKey.clear();
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
