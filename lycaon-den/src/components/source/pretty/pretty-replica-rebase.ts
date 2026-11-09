import type { ChangeDesc } from "@codemirror/state";
import { buildPrettySourceMap } from "./pretty-map.ts";
import type {
  PrettyCaretAffinity,
  PrettyDisplayChange,
  PrettyMappedSourceChange,
  PrettyOriginRun,
  PrettyProjectionResult,
  PrettyRawRange,
  PrettyRetainedEditIntention,
} from "./pretty-types.ts";

export interface PrettyRevisionDelta {
  readonly incarnation: string;
  readonly epoch: number;
  readonly fromRevision: number;
  readonly toRevision: number;
  readonly changes: ChangeDesc;
}

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
  readonly rawFrom: number;
  readonly rawTo: number;
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
      !Array.isArray(parsed.rawSegmentRanges)
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
        typeof c.rawFrom !== "number" ||
        typeof c.rawTo !== "number" ||
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
        rawFrom: c.rawFrom,
        rawTo: c.rawTo,
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

    if (!parsed.rawSegmentRanges.every(isValidRawRange)) {
      return null;
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

export function clearPrettyDocumentRecoveryState(documentId: string, incarnation?: string): void {
  const storage = activeRecoveryStorage();
  if (incarnation !== undefined) {
    recoveryByKey.delete(recoveryMapKey(documentId, incarnation));
    const storageKey = prettyRecoveryStorageKey(documentId, incarnation);
    try {
      storage?.removeItem(storageKey);
    } catch {
      // Ignore storage cleanup error.
    }
    trackedStorageKeys.delete(storageKey);
    return;
  }
  for (const [k, v] of recoveryByKey.entries()) {
    if (v.documentId === documentId) {
      recoveryByKey.delete(k);
      const storageKey = prettyRecoveryStorageKey(v.documentId, v.incarnation);
      try {
        storage?.removeItem(storageKey);
      } catch {
        // Ignore storage cleanup error.
      }
      trackedStorageKeys.delete(storageKey);
    }
  }
}

/**
 * Simulates a renderer/process loss by clearing only the in-memory recovery map while
 * preserving durable storage so restored replica incarnations reload their recovery state.
 */
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

export class PrettyRevisionDeltaRingBuffer {
  private readonly entries: PrettyRevisionDelta[] = [];
  private invalidatedBelowRevision = 0;

  constructor(readonly capacity = 64) {}

  recordStep(delta: PrettyRevisionDelta): void {
    if (
      this.entries.length > 0 &&
      this.entries[this.entries.length - 1]!.toRevision !== delta.fromRevision
    ) {
      this.entries.length = 0;
      this.invalidatedBelowRevision = delta.fromRevision;
    }
    this.entries.push(delta);
    while (this.entries.length > this.capacity) {
      const removed = this.entries.shift()!;
      this.invalidatedBelowRevision = Math.max(this.invalidatedBelowRevision, removed.toRevision);
    }
  }

  invalidateAll(atRevision: number): void {
    this.entries.length = 0;
    this.invalidatedBelowRevision = atRevision;
  }

  evictBefore(revision: number): void {
    while (this.entries.length > 0 && this.entries[0]!.fromRevision < revision) {
      const removed = this.entries.shift()!;
      this.invalidatedBelowRevision = Math.max(this.invalidatedBelowRevision, removed.toRevision);
    }
  }

  collectChain(
    baseIncarnation: string,
    baseEpoch: number,
    fromRevision: number,
    currentIncarnation: string,
    currentEpoch: number,
    toRevision: number,
  ): readonly PrettyRevisionDelta[] | null {
    if (baseIncarnation !== currentIncarnation || baseEpoch !== currentEpoch) return null;
    if (fromRevision === toRevision) return [];
    if (fromRevision > toRevision || fromRevision < this.invalidatedBelowRevision) return null;
    const chain: PrettyRevisionDelta[] = [];
    let expected = fromRevision;
    for (const entry of this.entries) {
      if (entry.toRevision <= fromRevision) continue;
      if (entry.fromRevision !== expected) return null;
      if (entry.incarnation !== currentIncarnation || entry.epoch !== currentEpoch) return null;
      chain.push(entry);
      expected = entry.toRevision;
      if (expected === toRevision) return chain;
    }
    return null;
  }
}

export type PrettyRebaseOutcome =
  | {
      readonly status: "disjoint";
      readonly changes: readonly PrettyMappedSourceChange[];
      readonly rebasedCursorOffset: number;
    }
  | {
      readonly status: "overlap-conflict" | "chain-unavailable";
      readonly rebasedCursorOffset: number;
    };

function rangesOverlapOrShareBoundary(
  localFrom: number,
  localTo: number,
  remoteFromA: number,
  remoteToA: number,
): boolean {
  if (localFrom === localTo) {
    // Zero-width insertion conflicts with same-offset insertion, deletion across localFrom, or boundary touch.
    return remoteFromA <= localFrom && localFrom <= remoteToA;
  }
  // Non-empty local replacement/deletion conflicts with any overlapping or interior remote change,
  // even when localFrom and localTo survive outside [remoteFromA, remoteToA).
  return remoteFromA <= localTo && remoteToA >= localFrom;
}

export function rebaseChangesOverDeltaChain(
  ring: PrettyRevisionDeltaRingBuffer,
  baseIncarnation: string,
  baseEpoch: number,
  baseRevision: number,
  currentIncarnation: string,
  currentEpoch: number,
  currentRevision: number,
  currentRawLength: number,
  changes: readonly PrettyMappedSourceChange[],
  fallbackCursorRawOffset: number,
): PrettyRebaseOutcome {
  const chain = ring.collectChain(
    baseIncarnation,
    baseEpoch,
    baseRevision,
    currentIncarnation,
    currentEpoch,
    currentRevision,
  );
  if (chain === null) {
    return {
      status: "chain-unavailable",
      rebasedCursorOffset: Math.max(0, Math.min(currentRawLength, fallbackCursorRawOffset)),
    };
  }
  if (chain.length === 0) {
    const first = changes[0];
    return {
      status: "disjoint",
      changes,
      rebasedCursorOffset: first ? first.from + first.insert.length : fallbackCursorRawOffset,
    };
  }

  let currentChanges = changes.map((c) => ({ ...c }));
  let cursor = fallbackCursorRawOffset;

  for (const step of chain) {
    let intersects = false;
    step.changes.iterChangedRanges((fromA, toA) => {
      for (const ch of currentChanges) {
        if (rangesOverlapOrShareBoundary(ch.from, ch.to, fromA, toA)) {
          intersects = true;
        }
      }
    });
    cursor = step.changes.mapPos(cursor, 1);
    if (intersects) {
      for (let idx = chain.indexOf(step) + 1; idx < chain.length; idx++) {
        cursor = chain[idx]!.changes.mapPos(cursor, 1);
      }
      return {
        status: "overlap-conflict",
        rebasedCursorOffset: Math.max(0, Math.min(currentRawLength, cursor)),
      };
    }
    currentChanges = currentChanges.map((ch) => ({
      from: step.changes.mapPos(ch.from, 1),
      to: step.changes.mapPos(ch.to, -1),
      insert: ch.insert,
    }));
  }

  const first = currentChanges[0];
  const finalCursor = first ? first.from + first.insert.length : cursor;
  return {
    status: "disjoint",
    changes: currentChanges,
    rebasedCursorOffset: Math.max(0, Math.min(currentRawLength, finalCursor)),
  };
}

function advanceOriginRunsSingleChange(
  runs: readonly PrettyOriginRun[],
  dFrom: number,
  dTo: number,
  rFrom: number,
  rTo: number,
  insertLength: number,
): PrettyOriginRun[] {
  const L = insertLength;
  const dispDelta = L - (dTo - dFrom);
  const rawDelta = L - (rTo - rFrom);
  const out: PrettyOriginRun[] = [];
  let insertedEmitted = false;

  const pushRun = (run: PrettyOriginRun): void => {
    if (run.srcFrom === run.srcTo && run.dispFrom === run.dispTo) return;
    const prev = out[out.length - 1];
    if (prev && prev.kind === run.kind && prev.srcTo === run.srcFrom && prev.dispTo === run.dispFrom) {
      if (
        prev.kind === "source" ||
        (prev.kind === "virtual-layout" && prev.srcFrom === run.srcTo)
      ) {
        out[out.length - 1] = {
          kind: prev.kind,
          srcFrom: prev.srcFrom,
          srcTo: run.srcTo,
          dispFrom: prev.dispFrom,
          dispTo: run.dispTo,
        };
        return;
      }
    }
    out.push(run);
  };

  const emitInsertIfNeeded = (): void => {
    if (insertedEmitted) return;
    insertedEmitted = true;
    if (L > 0) {
      pushRun({
        kind: "source",
        srcFrom: rFrom,
        srcTo: rFrom + L,
        dispFrom: dFrom,
        dispTo: dFrom + L,
      });
    }
  };

  for (const run of runs) {
    if (run.dispTo <= dFrom) {
      pushRun(run);
      continue;
    }
    if (run.dispFrom >= dTo) {
      emitInsertIfNeeded();
      pushRun({
        kind: run.kind,
        srcFrom: run.srcFrom + rawDelta,
        srcTo: run.srcTo + rawDelta,
        dispFrom: run.dispFrom + dispDelta,
        dispTo: run.dispTo + dispDelta,
      });
      continue;
    }
    if (run.dispFrom < dFrom) {
      pushRun({
        kind: run.kind,
        srcFrom: run.srcFrom,
        srcTo: rFrom,
        dispFrom: run.dispFrom,
        dispTo: dFrom,
      });
    }
    emitInsertIfNeeded();
    if (run.dispTo > dTo) {
      pushRun({
        kind: run.kind,
        srcFrom: rTo + rawDelta,
        srcTo: run.srcTo + rawDelta,
        dispFrom: dTo + dispDelta,
        dispTo: run.dispTo + dispDelta,
      });
    }
  }
  emitInsertIfNeeded();
  return out;
}

/**
 * Advances the active PrettyProjectionResult in-place across an intermediate IME composition
 * transaction so that `projection.displayText` and `projection.sourceMap` remain strictly
 * aligned with `view.state.doc` without replacing the browser's active composing DOM node.
 */
export function advanceProjectionForComposeFrame(
  prevProjection: PrettyProjectionResult,
  displayChanges: readonly PrettyDisplayChange[],
  rawChanges: readonly PrettyMappedSourceChange[],
  nextRawText: string,
  nextDisplayText: string,
  nextRevision: number,
): PrettyProjectionResult {
  let runs: readonly PrettyOriginRun[] = prevProjection.sourceMap.runs;
  if (displayChanges.length === 1 && rawChanges.length === 1) {
    const dc = displayChanges[0]!;
    const rc = rawChanges[0]!;
    runs = advanceOriginRunsSingleChange(
      runs,
      dc.from,
      dc.to,
      rc.from,
      rc.to,
      dc.insert.length,
    );
  }
  const nextMap = buildPrettySourceMap(nextRawText, nextDisplayText, runs);
  return {
    ...prevProjection,
    displayText: nextDisplayText,
    sourceMap: {
      ...nextMap,
      sourceIdentity: prevProjection.sourceMap.sourceIdentity,
      sourceRevision: nextRevision,
    },
  };
}
