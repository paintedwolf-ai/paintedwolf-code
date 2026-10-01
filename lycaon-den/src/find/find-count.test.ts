import { describe, expect, it } from "vitest";
import { formatFindCountLabel } from "./find-controller.ts";

describe("formatFindCountLabel", () => {
  it("renders N of M, No results, and 999+", () => {
    expect(
      formatFindCountLabel({ total: 47, activeIndex: 2, capped: false }),
    ).toBe("3 of 47");
    expect(
      formatFindCountLabel({ total: 0, activeIndex: -1, capped: false }),
    ).toBe("No results");
    expect(
      formatFindCountLabel({ total: 10_000, activeIndex: 0, capped: true }),
    ).toBe("999+");
  });
});
