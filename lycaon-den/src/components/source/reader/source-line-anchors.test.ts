import { describe, expect, it } from "vitest";
import { sourceLineAnchors } from "./source-line-anchors.ts";

describe("source line anchors", () => {
  it("coalesces consecutive unchanged lines and preserves exact newline boundaries", () => {
    const before = "old\r\na\r\nb\r\nlast", after = "new\r\na\r\nb\r\nlast";
    expect(sourceLineAnchors(before, after)).toEqual([{ fromA: 5, toA: before.length, fromB: 5, toB: after.length }]);
  });
  it("selects an ordered sequence when lines move", () => {
    const before = "a\nb\nc\nd\n", after = "b\na\nc\nd\n";
    const anchors = sourceLineAnchors(before, after);
    expect(anchors.reduce((size, anchor) => size + anchor.toA - anchor.fromA, 0)).toBe(6);
    for (const [i, anchor] of anchors.entries()) {
      expect(before.slice(anchor.fromA, anchor.toA)).toBe(after.slice(anchor.fromB, anchor.toB));
      if (i) {
        expect(anchor.fromA).toBeGreaterThanOrEqual(anchors[i - 1]!.toA);
        expect(anchor.fromB).toBeGreaterThanOrEqual(anchors[i - 1]!.toB);
      }
    }
  });
  it("leaves ambiguous repeated lines to the bounded diff", () => {
    expect(sourceLineAnchors("x\nx\n", "x\ny\nx\n")).toEqual([]);
    expect(sourceLineAnchors("x\n", "x\nx\n")).toEqual([]);
    expect(sourceLineAnchors("", "new")).toEqual([]);
  });
});
