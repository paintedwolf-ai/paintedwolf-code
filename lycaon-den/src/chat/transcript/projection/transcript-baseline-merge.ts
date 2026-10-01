import type { Message } from "../../../api/types.ts";

export type TranscriptBaselineSlice = {
  transcriptSessionId?: string;
  messages: readonly Message[];
  transcriptWatermark: number;
};

/** Merge a server transcript page with locally-held rows newer than the watermark. */
export function mergeTranscriptBaseline(
  sessionId: string,
  page: readonly Message[],
  watermark: number,
  current: TranscriptBaselineSlice,
): { messages: Message[]; sessionChanged: boolean } {
  const sameSession = current.transcriptSessionId === sessionId;
  if (!sameSession) {
    return { messages: page.slice(), sessionChanged: true };
  }

  const next = page.slice();
  const indexById = new Map(next.map((m, i) => [m.id, i] as const));
  for (const row of current.messages) {
    if ((row.seq ?? 0) <= watermark) continue;
    const idx = indexById.get(row.id);
    if (idx == null) {
      next.push(row);
    } else {
      next[idx] = row;
    }
  }
  return { messages: next, sessionChanged: false };
}
