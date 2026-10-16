import type { TranscriptOrdSpan } from "../layout/transcript-window.ts";

/** Ords within the reader's viewport + prefetch reach for each session. */
const reachBySession = new Map<string, TranscriptOrdSpan>();

export function publishTranscriptReaderReach(
  sessionId: string,
  span: TranscriptOrdSpan | undefined,
): void {
  const id = sessionId.trim();
  if (!id) return;
  if (!span) {
    reachBySession.delete(id);
    return;
  }
  reachBySession.set(id, span);
}

export function transcriptReaderReach(sessionId: string): TranscriptOrdSpan | undefined {
  return reachBySession.get(sessionId.trim());
}
