// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { placeAnchoredSurface } from "./AnchoredSurface.tsx";

const viewport = { width: 800, height: 600 };

describe("placeAnchoredSurface", () => {
  it("keeps full-height resize tips below window chrome", () => {
    const result = placeAnchoredSurface(
      { top: 0, right: 280, bottom: 600, left: 276, width: 4, height: 600 },
      { width: 320, height: 40 }, viewport,
      { preferredSide: "top", height: "content", topInset: 30 },
    );
    expect(result.top).toBe(38);
    expect(result.maxHeight).toBe(40);
  });

  it("keeps the preferred side when the surface fits", () => {
    expect(
      placeAnchoredSurface(
        { top: 100, right: 220, bottom: 130, left: 100, width: 120, height: 30 },
        { width: 180, height: 200 },
        viewport,
        { preferredSide: "bottom", align: "start", gap: 4 },
      ),
    ).toMatchObject({ side: "bottom", left: 100, top: 134 });
  });

  it("flips above when the bottom would cross the viewport", () => {
    expect(
      placeAnchoredSurface(
        { top: 520, right: 760, bottom: 550, left: 700, width: 60, height: 30 },
        { width: 180, height: 200 },
        viewport,
        { preferredSide: "bottom", align: "end", gap: 4 },
      ),
    ).toMatchObject({ side: "top", left: 580, top: 316 });
  });

  it("shifts along the cross axis instead of spilling out the side", () => {
    const result = placeAnchoredSurface(
      { top: 100, right: 795, bottom: 130, left: 755, width: 40, height: 30 },
      { width: 240, height: 100 },
      viewport,
      { preferredSide: "bottom", align: "start" },
    );
    expect(result.left).toBe(552);
    expect(result.left + result.maxWidth).toBeLessThanOrEqual(792);
  });

  it("constrains an oversized surface to the room on its chosen side", () => {
    const result = placeAnchoredSurface(
      { top: 260, right: 420, bottom: 290, left: 380, width: 40, height: 30 },
      { width: 1000, height: 1000 },
      viewport,
      { preferredSide: "bottom", gap: 4 },
    );
    expect(result.side).toBe("bottom");
    expect(result.maxWidth).toBe(784);
    expect(result.maxHeight).toBe(298);
    expect(result.left).toBe(8);
    expect(result.top).toBe(294);
  });

  it("lets menus straddle their anchor before viewport overflow is needed", () => {
    const result = placeAnchoredSurface(
      { top: 300, right: 400, bottom: 300, left: 400, width: 0, height: 0 },
      { width: 240, height: 500 },
      viewport,
      { preferredSide: "bottom", height: "viewport" },
    );
    expect(result.maxHeight).toBe(500);
    expect(result.top).toBe(92);
  });

  it("uses the same collision rules for horizontal popovers", () => {
    expect(
      placeAnchoredSurface(
        { top: 200, right: 780, bottom: 230, left: 750, width: 30, height: 30 },
        { width: 240, height: 160 },
        viewport,
        { preferredSide: "right", align: "center" },
      ),
    ).toMatchObject({ side: "left", left: 506, top: 135 });
  });

  it("rounds a fractional surface up so one that fits never scrolls", () => {
    const result = placeAnchoredSurface(
      { top: 100, right: 220, bottom: 130, left: 100, width: 120, height: 30 },
      { width: 268.4375, height: 201.9375 },
      viewport,
      { preferredSide: "right", align: "start", gap: 6 },
    );
    expect(result.maxHeight).toBe(202);
    expect(result.maxWidth).toBe(269);
  });

  it("preserves intrinsic height for content-sized surfaces", () => {
    const result = placeAnchoredSurface(
      { top: 260, right: 420, bottom: 290, left: 380, width: 40, height: 30 },
      { width: 320, height: 1000 },
      viewport,
      { preferredSide: "right", height: "content" },
    );
    expect(result.maxHeight).toBe(1000);
    expect(result.top).toBe(8);
  });
});
