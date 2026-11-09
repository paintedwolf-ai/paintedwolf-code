import type { ChangeDesc } from "@codemirror/state";
import type {
  PrettyAmbiguousMultiCursorConflict,
  PrettyCaretAffinity,
  PrettyMappedSourceChange,
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

export interface PrettyPendingStaleEdit {
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
  pendingConflict: PrettyAmbiguousMultiCursorConflict | null;
  pendingStaleEdit: PrettyPendingStaleEdit | null;
  rawSegmentRanges: PrettyRawRange[];
  caretAffinity: PrettyCaretAffinityState | null;
}

const recoveryByDocumentId = new Map<string, PrettyDocumentRecoveryState>();

export function getPrettyDocumentRecoveryState(documentId: string): PrettyDocumentRecoveryState {
  let state = recoveryByDocumentId.get(documentId);
  if (!state) {
    state = {
      pendingConflict: null,
      pendingStaleEdit: null,
      rawSegmentRanges: [],
      caretAffinity: null,
    };
    recoveryByDocumentId.set(documentId, state);
  }
  return state;
}

export function clearPrettyDocumentRecoveryState(documentId: string): void {
  recoveryByDocumentId.delete(documentId);
}

export function resetPrettyDocumentRecoveryStoreForTests(): void {
  recoveryByDocumentId.clear();
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
