import { describe, expect, it } from "vitest";
import { paginateSlice, paginationWindow } from "./pagination.ts";

describe("pagination", () => {
  it("clamps pages and returns the visible slice", () => {
    expect(paginateSlice(["a", "b", "c"], 9, 2)).toEqual({
      slice: ["c"],
      page: 1,
      total: 3,
      pageCount: 2,
      from: 3,
      to: 3,
    });
  });

  it("returns an empty window for invalid or empty input", () => {
    expect(paginationWindow(3, 0, 0)).toEqual({
      page: 0,
      total: 0,
      pageCount: 0,
      from: 0,
      to: 0,
    });
    expect(paginateSlice([], 0, 10).slice).toEqual([]);
  });
});
