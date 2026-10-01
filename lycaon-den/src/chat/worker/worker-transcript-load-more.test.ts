import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import {
  applyTranscriptPage,
  emptyTranscriptWindow,
  installTailWindow,
  transcriptPageRequest,
  type TranscriptWindow,
} from "../transcript/layout/transcript-window.ts";
import { workerTranscriptEdgeInReach } from "./worker-transcript-load-more.ts";

function rows(from: number, to: number): Message[] {
  return Array.from({ length: to - from + 1 }, (_, i) => ({
    id: `m${from + i}`, ord: from + i, seq: from + i, content: "", created_at: "t",
    role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
  }));
}

function page(messages: Message[], before: boolean, after: boolean) {
  return { messages, hasMoreBefore: before, hasMoreAfter: after };
}

const viewport = { top: 100, bottom: 900 };

describe("worker transcript edge in reach", () => {
  const withHistory = installTailWindow(emptyTranscriptWindow(), page(rows(901, 1000), true, false), { resetPages: true });
  const withGap: TranscriptWindow = applyTranscriptPage(
    withHistory,
    transcriptPageRequest(withHistory, "start")!,
    page(rows(1, 100), false, true),
  )!;

  it("reads from the first row when the reader meets the activity from above", () => {
    expect(workerTranscriptEdgeInReach(withHistory, { top: 400, bottom: 5000 }, viewport, { fromAbove: true })).toBe("start");
  });

  it("extends older history only for a reader inside the activity", () => {
    expect(workerTranscriptEdgeInReach(withHistory, { top: 400, bottom: 5000 }, viewport, { fromAbove: false })).toBeUndefined();
    expect(workerTranscriptEdgeInReach(withHistory, { top: -1000, bottom: 5000 }, viewport, { fromAbove: false })).toBe("older");
    // Beyond two viewports of lookahead the reader is not near the edge yet.
    expect(workerTranscriptEdgeInReach(withHistory, { top: -1600, bottom: 5000 }, viewport, { fromAbove: false })).toBeUndefined();
  });

  it("reads toward the tail when the end of contiguous history comes into reach", () => {
    expect(workerTranscriptEdgeInReach(withGap, { top: -4000, bottom: 2000 }, viewport, { fromAbove: false })).toBe("newer");
    expect(workerTranscriptEdgeInReach(withGap, { top: -4000, bottom: 3000 }, viewport, { fromAbove: false })).toBeUndefined();
    // A reader below the activity is not reading toward its end.
    expect(workerTranscriptEdgeInReach(withGap, { top: -4000, bottom: 50 }, viewport, { fromAbove: false })).toBeUndefined();
  });

  it("asks for nothing once the whole transcript is resident", () => {
    const whole = installTailWindow(emptyTranscriptWindow(), page(rows(1, 100), false, false), { resetPages: true });
    expect(workerTranscriptEdgeInReach(whole, { top: 400, bottom: 900 }, viewport, { fromAbove: true })).toBeUndefined();
  });
});
