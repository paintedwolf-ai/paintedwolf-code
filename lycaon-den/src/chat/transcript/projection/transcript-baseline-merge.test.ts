import { describe, expect, it } from "vitest";
import { mergeTranscriptBaseline } from "./transcript-baseline-merge.ts";
import type { Message } from "../../../api/types.ts";

const row = (id: string, content: string, seq?: number): Message => ({
  id,
  role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
  content,
  created_at: "t",
  seq,
});

describe("mergeTranscriptBaseline", () => {
  it("replaces wholesale when the session id changes", () => {
    const merged = mergeTranscriptBaseline(
      "sess-b",
      [row("m1", "fresh")],
      1,
      {
        transcriptSessionId: "sess-a",
        messages: [row("old", "stale")],
        transcriptWatermark: 0,
      },
    );
    expect(merged.sessionChanged).toBe(true);
    expect(merged.messages).toEqual([row("m1", "fresh")]);
  });

  it("keeps local rows newer than the page watermark", () => {
    const merged = mergeTranscriptBaseline(
      "sess-a",
      [row("m1", "page", 1)],
      1,
      {
        transcriptSessionId: "sess-a",
        messages: [row("m1", "page", 1), row("m2", "live", 9)],
        transcriptWatermark: 8,
      },
    );
    expect(merged.sessionChanged).toBe(false);
    expect(merged.messages.map((m) => m.id)).toEqual(["m1", "m2"]);
  });
});
