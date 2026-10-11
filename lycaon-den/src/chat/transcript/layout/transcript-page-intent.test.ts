import { describe, expect, it } from "vitest";
import { TranscriptPageIntent } from "./transcript-page-intent.ts";

describe("transcript page intent", () => {
  it("waits for reader direction when both resident edges are in reach", () => {
    const intent = new TranscriptPageIntent();
    expect(intent.edge(true, true)).toBeUndefined();
    intent.noteInput("older");
    expect(intent.edge(true, true)).toBe("older");
    expect(intent.edge(false, true)).toBeUndefined();
    intent.noteInput("newer");
    expect(intent.edge(true, true)).toBe("newer");
  });

  it("cannot reverse direction when repeated page eviction changes geometry", () => {
    const intent = new TranscriptPageIntent();
    expect(intent.edge(true, false)).toBe("older");
    for (let cycle = 0; cycle < 100; cycle++) {
      expect(intent.edge(false, true)).toBeUndefined();
      expect(intent.edge(true, true)).toBe("older");
      intent.advanced();
    }
  });

  it("bounds repeated failures and retries only after renewed reader input", () => {
    const intent = new TranscriptPageIntent();
    expect(intent.retryDelay("older:a", 0)).toBe(0);
    intent.failed("older:a", 0);
    expect(intent.retryDelay("older:a", 10_000)).toBeUndefined();
    intent.noteInput("older");
    expect(intent.retryDelay("older:a", 100)).toBe(150);
    expect(intent.retryDelay("older:a", 250)).toBe(0);
    for (let attempt = 0; attempt < 100; attempt++) {
      intent.failed("older:a", 10_000);
      intent.noteInput("older");
      expect(intent.retryDelay("older:a", 10_000)).toBeLessThanOrEqual(8_000);
    }
    expect(intent.retryDelay("older:b", 10_000)).toBe(0);
    intent.advanced();
    expect(intent.retryDelay("older:a", 10_000)).toBe(0);
  });
});
