import type { ChangeDesc } from "@codemirror/state";
import { buildPrettySourceMap } from "./pretty-map.ts";
import type {
  PrettyDisplayChange,
  PrettyMappedSourceChange,
  PrettyOriginRun,
  PrettyProjectionResult,
} from "./pretty-types.ts";

export interface PrettyRevisionDelta {
  readonly incarnation: string;
  readonly epoch: number;
  readonly fromRevision: number;
  readonly toRevision: number;
  readonly changes: ChangeDesc;
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
      const removed = this.entries.shift();
      if (removed) {
        this.invalidatedBelowRevision = Math.max(this.invalidatedBelowRevision, removed.toRevision);
      }
    }
  }

  invalidateAll(atRevision: number): void {
    this.entries.length = 0;
    this.invalidatedBelowRevision = atRevision;
  }

  evictBefore(revision: number): void {
    while (this.entries.length > 0 && this.entries[0]!.fromRevision < revision) {
      const removed = this.entries.shift();
      if (removed) {
        this.invalidatedBelowRevision = Math.max(this.invalidatedBelowRevision, removed.toRevision);
      }
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
    return remoteFromA <= localFrom && localFrom <= remoteToA;
  }
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
    if (
      prev &&
      prev.kind === run.kind &&
      prev.segmentIndex === run.segmentIndex &&
      prev.srcTo === run.srcFrom &&
      prev.dispTo === run.dispFrom
    ) {
      if (
        prev.kind !== "virtual-layout" ||
        (prev.srcFrom === run.srcTo && prev.insertOffset === run.insertOffset)
      ) {
        out[out.length - 1] = {
          kind: prev.kind,
          srcFrom: prev.srcFrom,
          srcTo: run.srcTo,
          dispFrom: prev.dispFrom,
          dispTo: run.dispTo,
          insertOffset: prev.insertOffset,
          segmentIndex: prev.segmentIndex,
        };
        return;
      }
    }
    out.push(run);
  };

  const emitInsertIfNeeded = (segmentIndex = 0): void => {
    if (insertedEmitted) return;
    insertedEmitted = true;
    if (L > 0) {
      pushRun({
        kind: "identity",
        srcFrom: rFrom,
        srcTo: rFrom + L,
        dispFrom: dFrom,
        dispTo: dFrom + L,
        insertOffset: rFrom + L,
        segmentIndex,
      });
    }
  };

  for (const run of runs) {
    if (run.dispTo <= dFrom) {
      pushRun(run);
      continue;
    }
    if (run.dispFrom >= dTo) {
      emitInsertIfNeeded(run.segmentIndex);
      pushRun({
        kind: run.kind,
        srcFrom: run.srcFrom + rawDelta,
        srcTo: run.srcTo + rawDelta,
        dispFrom: run.dispFrom + dispDelta,
        dispTo: run.dispTo + dispDelta,
        insertOffset: run.insertOffset + rawDelta,
        segmentIndex: run.segmentIndex,
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
        insertOffset: Math.min(run.insertOffset, rFrom),
        segmentIndex: run.segmentIndex,
      });
    }
    emitInsertIfNeeded(run.segmentIndex);
    if (run.dispTo > dTo) {
      pushRun({
        kind: run.kind,
        srcFrom: rTo + rawDelta,
        srcTo: run.srcTo + rawDelta,
        dispFrom: dTo + dispDelta,
        dispTo: run.dispTo + dispDelta,
        insertOffset: Math.max(rTo, run.insertOffset) + rawDelta,
        segmentIndex: run.segmentIndex,
      });
    }
  }
  emitInsertIfNeeded(0);
  return out;
}

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
  const nextMap = buildPrettySourceMap(nextRevision, nextRawText, nextDisplayText, runs);
  return {
    ...prevProjection,
    displayText: nextDisplayText,
    sourceMap: nextMap,
  };
}
