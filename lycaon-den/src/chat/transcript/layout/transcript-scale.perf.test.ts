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

describe("transcript hydration performance", () => {
  it("hydrates the same number of rows from a tail window independent of N", () => {
    const run = (n: number) => {
      const start = Math.max(1, n - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
      const tail = messagesInRange(start, n, "h");
      const tw = installTailWindow(
        emptyTranscriptWindow(),
        {
          messages: tail,
          hasMoreBefore: n > DEFAULT_TRANSCRIPT_PAGE_LIMIT,
          hasMoreAfter: false,
        },
        { resetPages: true },
      );
      return materializeTranscriptWindow(tw);
    };
    for (const n of [PERF_N, PERF_N_DOUBLE]) {
      const rows = run(n);
      expect(rows).toHaveLength(DEFAULT_TRANSCRIPT_PAGE_LIMIT);
      expect(rows.at(-1)?.ord).toBe(n);
    }
  });
});
