import { describe, expect, it } from "vitest";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  emptyTranscriptWindow,
  installTailWindow,
  materializeTranscriptWindow,
} from "./transcript-window.ts";
import { messagesInRange } from "./transcript-scale-fixture.ts";

const PERF_N = 5000;
const PERF_N_DOUBLE = 10_000;
const TRANSCRIPT_HYDRATE_CEILING_MS = 100;

describe("transcript hydration performance", () => {
  it("hydrates a tail window under the recorded ceiling independent of N", () => {
    const run = (n: number) => {
      const start = Math.max(1, n - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
      const tail = messagesInRange(start, n, "h");
      const t0 = performance.now();
      const tw = installTailWindow(
        emptyTranscriptWindow(),
        {
          messages: tail,
          hasMoreBefore: n > DEFAULT_TRANSCRIPT_PAGE_LIMIT,
          hasMoreAfter: false,
        },
        { resetPages: true },
      );
      materializeTranscriptWindow(tw);
      return performance.now() - t0;
    };
    const ms5k = run(PERF_N);
    const ms10k = run(PERF_N_DOUBLE);
    expect(ms5k).toBeLessThanOrEqual(TRANSCRIPT_HYDRATE_CEILING_MS);
    expect(ms10k).toBeLessThanOrEqual(TRANSCRIPT_HYDRATE_CEILING_MS);
    // Tail hydration stays bounded as session length grows.
    expect(ms10k).toBeLessThanOrEqual(Math.max(ms5k * 3, 20));
  });
});
