import { describe, expect, it } from "vitest";
import { adjustedTokens, NO_SYSTEM_ADJUSTMENTS } from "./theme-adjustments.ts";
import { contrastRatio, parseHexColor } from "./theme-color.ts";

const DAYLIGHT = {
  background: "#ffffff",
  shadow: "#1c1b1a",
  text: "#1c1b1a",
  "text-muted": "#6e6c67",
  border: "#d3d2cf",
  accent: "#b85c38",
  "accent-text": "#9d4e2b",
};

const ratioOnPage = (value: string | undefined) => {
  const page = parseHexColor(DAYLIGHT.background)!;
  return contrastRatio(parseHexColor(value ?? "")!, page, page);
};

describe("system theme adjustments", () => {
  it("leaves the theme as authored without adjustments", () => {
    expect(adjustedTokens(DAYLIGHT, NO_SYSTEM_ADJUSTMENTS)).toEqual({});
  });

  it("rebuilds the accent family from the chosen system accent", () => {
    const out = adjustedTokens(DAYLIGHT, { accent: "#007aff", increaseContrast: false });
    expect(out.accent).toBe("#007aff");
    expect(out["accent-warm"]).toBe("#007aff");
    // Deepened toward shadow, as the theme compiler derives accent-hover.
    expect(out["accent-hover"]).toBe("#036fe4");
    // Dark ink out-contrasts white on system blue, as the compiler would choose.
    expect(out["on-accent"]).toBe("#141312");
    expect(ratioOnPage(out["accent-text"])).toBeGreaterThanOrEqual(4.5);
    expect(out["text-muted"]).toBeUndefined();
  });

  it("raises secondary copy and boundaries under Increase Contrast", () => {
    const out = adjustedTokens(DAYLIGHT, { accent: null, increaseContrast: true });
    expect(ratioOnPage(out["text-muted"])).toBeGreaterThanOrEqual(7);
    expect(ratioOnPage(out["accent-text"])).toBeGreaterThanOrEqual(7);
    expect(ratioOnPage(out.border)).toBeGreaterThanOrEqual(3);
    expect(out.accent).toBeUndefined();
  });
});
