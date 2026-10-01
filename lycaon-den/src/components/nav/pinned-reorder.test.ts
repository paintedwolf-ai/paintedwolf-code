import { describe, expect, it } from "vitest";
import { pinnedDragOffset, pinnedDropIndex, pinnedRowShift } from "./pinned-reorder.ts";

describe("pinned reorder geometry", () => {
  it("lands on the nearest slot and stays inside the list", () => {
    expect(pinnedDropIndex(0, 10, 25, 4)).toBe(0);
    expect(pinnedDropIndex(0, 13, 25, 4)).toBe(1);
    expect(pinnedDropIndex(3, -60, 25, 4)).toBe(1);
    expect(pinnedDropIndex(1, 500, 25, 4)).toBe(3);
    expect(pinnedDropIndex(1, -500, 25, 4)).toBe(0);
  });

  it("steps the rows between the two slots aside", () => {
    // Dragging row 0 down to slot 2: rows 1 and 2 move up.
    expect([0, 1, 2, 3].map((i) => pinnedRowShift(i, 0, 2, 25))).toEqual([0, -25, -25, 0]);
    // Dragging row 3 up to slot 1: rows 1 and 2 move down.
    expect([0, 1, 2, 3].map((i) => pinnedRowShift(i, 3, 1, 25))).toEqual([0, 25, 25, 0]);
  });

  it("lets the dragged row overshoot either end by a quarter row", () => {
    expect(pinnedDragOffset(0, -100, 24, 3)).toBe(-6);
    expect(pinnedDragOffset(0, 100, 24, 3)).toBe(54);
    expect(pinnedDragOffset(1, 5, 24, 3)).toBe(5);
  });
});
