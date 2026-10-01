import type { Message } from "../../../api/types.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_PAGE_BUDGET,
  emptyTranscriptWindow,
  installTailWindow,
  loadOlderWindow,
  type TranscriptWindow,
} from "./transcript-window.ts";

function msg(id: string, ord: number): Message {
  const user = ord % 2 === 0;
  return {
    id,
    role: user ? "user" : "assistant",
    origin: user ? "user" : "model",
    authority: user ? "user" : "none",
    trust_tier: "trusted",
    content: `row ${ord}`,
    ord,
    seq: ord,
    created_at: "2026-01-01T00:00:00Z",
  };
}

export function messagesInRange(start: number, end: number, prefix = "m"): Message[] {
  return Array.from({ length: Math.max(0, end - start + 1) }, (_, index) => {
    const ord = start + index;
    return msg(`${prefix}-${ord}`, ord);
  });
}

// Only the bounded window is allocated, regardless of session length.
export function sparseWindowForSessionLength(n: number): TranscriptWindow {
  const tailStart = Math.max(1, n - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
  const tail = messagesInRange(tailStart, n);
  let tw = installTailWindow(
    emptyTranscriptWindow(),
    {
      messages: tail,
      hasMoreBefore: n > DEFAULT_TRANSCRIPT_PAGE_LIMIT,
      hasMoreAfter: false,
    },
    { resetPages: true },
  );
  // Fill the page budget to exercise maximum residency.
  let cursor = tail[0]?.ord;
  for (let p = 0; p < TRANSCRIPT_PAGE_BUDGET && cursor != null && cursor > 1; p++) {
    const end = cursor - 1;
    const start = Math.max(1, end - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
    const rows = messagesInRange(start, end);
    if (rows.length === 0) break;
    tw = loadOlderWindow(tw, {
      messages: rows,
      hasMoreBefore: start > 1,
      hasMoreAfter: true,
    });
    cursor = rows[0]?.ord;
  }
  return tw;
}
