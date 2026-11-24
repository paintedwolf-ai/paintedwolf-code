import type { TranscriptOrdSpan } from "../layout/transcript-window.ts";

/** Ords within the reader's viewport + prefetch reach for each session. */
const reachBySession = new Map<string, TranscriptOrdSpan>();

type AdmittedReach = {
  span: TranscriptOrdSpan;
  edge: "older" | "newer" | "seek";
  settled: boolean;
};

/** Newly admitted page span protected through anchor settlement. */
const admittedReachBySession = new Map<string, AdmittedReach>();

function spansOverlap(a: TranscriptOrdSpan, b: TranscriptOrdSpan): boolean {
  return a.firstOrd <= b.lastOrd && a.lastOrd >= b.firstOrd;
}

function mergeSpans(a: TranscriptOrdSpan | undefined, b: TranscriptOrdSpan | undefined): TranscriptOrdSpan | undefined {
  if (!a) return b;
  if (!b) return a;
  return {
    firstOrd: Math.min(a.firstOrd, b.firstOrd),
    lastOrd: Math.max(a.lastOrd, b.lastOrd),
  };
}

export function publishTranscriptReaderReach(
  sessionId: string,
  span: TranscriptOrdSpan | undefined,
): void {
  const id = sessionId.trim();
  if (!id) return;
  if (!span) {
    reachBySession.delete(id);
    admittedReachBySession.delete(id);
    return;
  }
  reachBySession.set(id, span);
}

export function publishAdmittedTranscriptReach(
  sessionId: string,
  span: TranscriptOrdSpan | undefined,
  edge: "older" | "newer" | "seek",
): void {
  const id = sessionId.trim();
  if (!id) return;
  if (!span) {
    admittedReachBySession.delete(id);
    return;
  }
  admittedReachBySession.set(id, { span, edge, settled: false });
}

export function markAdmittedTranscriptReachSettled(sessionId: string): void {
  const id = sessionId.trim();
  if (!id) return;
  const entry = admittedReachBySession.get(id);
  if (entry) entry.settled = true;
}

export function releaseAdmittedTranscriptReach(
  sessionId: string,
  movementEdge?: "older" | "newer",
): void {
  const id = sessionId.trim();
  if (!id) return;
  if (!movementEdge) {
    admittedReachBySession.delete(id);
    return;
  }
  const entry = admittedReachBySession.get(id);
  if (!entry || !entry.settled) return;
  const reach = reachBySession.get(id);
  if (movementEdge !== entry.edge || (reach && spansOverlap(reach, entry.span))) {
    admittedReachBySession.delete(id);
  }
}

export function transcriptReaderReach(sessionId: string): TranscriptOrdSpan | undefined {
  const id = sessionId.trim();
  return mergeSpans(reachBySession.get(id), admittedReachBySession.get(id)?.span);
}
