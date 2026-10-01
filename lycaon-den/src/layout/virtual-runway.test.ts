import { describe, expect, it } from "vitest";
import { bufferedVirtualRange } from "./virtual-runway.ts";

describe("bufferedVirtualRange", () => {
  it("fills a pixel runway even when rows have different heights", () => {
    const indexes = bufferedVirtualRange({
      range: { startIndex: 10, endIndex: 12, overscan: 2, count: 30 },
      bufferPx: 120,
      rowSize: (index) => (index % 2 === 0 ? 60 : 30),
      gapPx: 10,
    });

    expect(indexes[0]).toBe(7);
    expect(indexes[indexes.length - 1]).toBe(15);
  });

  it("clamps the runway at collection boundaries", () => {
    expect(
      bufferedVirtualRange({
        range: { startIndex: 0, endIndex: 1, overscan: 6, count: 4 },
        bufferPx: 1_000,
        rowSize: () => 40,
      }),
    ).toEqual([0, 1, 2, 3]);
  });
});
