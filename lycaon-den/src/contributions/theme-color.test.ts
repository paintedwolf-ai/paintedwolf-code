import { assert, describe, expect, it } from "vitest";
import {
  contrastRatio,
  formatHexColor,
  legibleInk,
  mixColors,
  parseHexColor,
  readableOn,
} from "./theme-color.ts";

const hex = (value: string) => {
  const parsed = parseHexColor(value);
  assert(parsed, `parse fixture color ${value}`);
  return parsed;
};

describe("theme color math", () => {
  it("mixes the way the theme compiler does", () => {
    // accent-hover as compiled for the stock daylight and charcoal themes.
    expect(formatHexColor(mixColors(hex("#1c1b1a"), 12, hex("#b85c38")))).toBe("#a55434");
    expect(formatHexColor(mixColors(hex("#000000"), 12, hex("#a85830")))).toBe("#944d2a");
  });

  it("round-trips hex with and without alpha", () => {
    expect(formatHexColor(hex("#A85830"))).toBe("#a85830");
    expect(formatHexColor(hex("#00000080"))).toBe("#00000080");
    expect(parseHexColor("rgb(0 0 0)")).toBeNull();
  });

  it("keeps a hue that already clears the floor", () => {
    const blue = hex("#0a5fd6");
    expect(legibleInk(blue, hex("#ffffff"), hex("#ffffff"), 4.5)).toEqual(blue);
  });

  it("pulls a weak hue toward the far pole until it clears the floor", () => {
    const yellow = hex("#ffc600");
    const white = hex("#ffffff");
    const ink = legibleInk(yellow, white, white, 4.5);
    expect(contrastRatio(ink, white, white)).toBeGreaterThanOrEqual(4.5);
    expect(ink).not.toEqual(yellow);
  });

  it("labels a light fill with dark ink and a deep fill with white", () => {
    const page = hex("#ffffff");
    expect(formatHexColor(readableOn(hex("#ffc600"), page))).toBe("#141312");
    expect(formatHexColor(readableOn(hex("#0a5fd6"), page))).toBe("#ffffff");
  });
});
