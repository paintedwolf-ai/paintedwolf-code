import { describe, expect, it } from "vitest";
import { selectionOutline } from "./selection-outline.ts";

describe("selection perimeter", () => {
  it("follows partial first and last lines without drawing across internal joins", () => {
    const edges = selectionOutline([
      { left: 40, top: 0, width: 60, height: 10 },
      { left: 0, top: 10, width: 100, height: 20 },
      { left: 0, top: 30, width: 30, height: 10 },
    ]);
    expect(edges).toHaveLength(8);
    expect(edges).toEqual(expect.arrayContaining([
      { x1: 40, y1: 0, x2: 100, y2: 0 },
      { x1: 0, y1: 10, x2: 40, y2: 10 },
      { x1: 30, y1: 30, x2: 100, y2: 30 },
      { x1: 0, y1: 40, x2: 30, y2: 40 },
      { x1: 40, y1: 0, x2: 40, y2: 10 },
      { x1: 100, y1: 0, x2: 100, y2: 30 },
      { x1: 0, y1: 10, x2: 0, y2: 40 },
      { x1: 30, y1: 30, x2: 30, y2: 40 },
    ]));
  });

  it("merges overlapping and duplicate rectangles into one perimeter", () => {
    const edges = selectionOutline([
      { left: 0, top: 0, width: 10, height: 10 },
      { left: 5, top: 0, width: 10, height: 10 },
      { left: 0, top: 0, width: 10, height: 10 },
    ]);
    expect(edges).toHaveLength(4);
    expect(edges).toContainEqual({ x1: 0, y1: 0, x2: 15, y2: 0 });
    expect(edges).toContainEqual({ x1: 15, y1: 0, x2: 15, y2: 10 });
  });

  it("keeps separated directional fragments disconnected", () => {
    const edges = selectionOutline([
      { left: 0, top: 0, width: 10, height: 10 },
      { left: 20, top: 0, width: 10, height: 10 },
    ]);
    expect(edges).toHaveLength(8);
    expect(edges.every(edge => edge.x2 <= 10 || edge.x1 >= 20)).toBe(true);
  });

  it("joins measured subpixel boundaries without hairline seams", () => {
    const edges = selectionOutline([
      { left: 0, top: 0, width: 20, height: 10 },
      { left: 0, top: 10.0000001, width: 20, height: 10 },
    ]);
    expect(edges).toHaveLength(4);
    expect(edges).toContainEqual({ x1: 20, y1: 0, x2: 20, y2: 20 });
    expect(selectionOutline([{ left: 0, top: 0, width: 0, height: 10 }])).toEqual([]);
  });
});
