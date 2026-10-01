import { describe, expect, it } from "vitest";
import { stepRailLayout, stepRailPosition, stepRailReveal, stepRailWindow } from "./step-rail-layout.ts";

describe("walk rail geometry", () => {
  it("fits short histories and stops compressing before targets overlap", () => {
    expect(stepRailLayout(6, 400).extent).toBe(400);
    for (const count of [2, 21, 240, 10_000]) {
      for (const width of [0, 30, 200, 736, 1200]) {
        const layout = stepRailLayout(count, width);
        expect(layout.pitch).toBeGreaterThanOrEqual(40);
        expect(stepRailPosition(layout, count - 1) + 12).toBe(layout.extent);
        expect(layout.extent).toBeGreaterThanOrEqual(width);
      }
    }
  });

  it("keeps rendering bounded at either end and in a very long history", () => {
    const layout = stepRailLayout(100_000, 736);
    for (const [offset, firstVisible, lastVisible] of [
      [0, 0, 18],
      [20_000, 500, 518],
      [layout.maxScroll, 99_981, 99_999],
    ] as const) {
      const range = stepRailWindow(layout, offset);
      expect(range.end - range.first).toBeLessThan(28);
      expect(range.first).toBeGreaterThanOrEqual(0);
      expect(range.end).toBeLessThanOrEqual(layout.count);
      expect(range.first).toBeLessThanOrEqual(firstVisible);
      expect(range.end).toBeGreaterThan(lastVisible);
    }
    expect(stepRailWindow(stepRailLayout(0, 200), 0)).toEqual({ first: 0, end: 0 });
    expect(stepRailWindow(stepRailLayout(1, 200), 0)).toEqual({ first: 0, end: 1 });
    expect(stepRailWindow(stepRailLayout(100_000, 0), 0)).toEqual({ first: 0, end: 4 });
  });

  it("holds every fully visible target still and reveals clipped selections", () => {
    const layout = stepRailLayout(240, 400);
    expect(stepRailReveal(layout, 0, 4)).toBe(0);
    expect(stepRailReveal(layout, 0, 9)).toBe(0);
    expect(stepRailReveal(layout, 0, 10)).toBe(72);
    expect(stepRailReveal(layout, 400, 10)).toBe(400);
    expect(stepRailReveal(layout, 400, 19)).toBe(400);
    expect(stepRailReveal(layout, 405, 10)).toBe(352);
    expect(stepRailReveal(layout, 400, 0)).toBe(0);
    expect(stepRailReveal(layout, 0, 239)).toBe(layout.maxScroll);
  });
});
