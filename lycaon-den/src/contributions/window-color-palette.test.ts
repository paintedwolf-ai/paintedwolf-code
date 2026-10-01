import { assert, describe, expect, it } from "vitest";
import { STOCK_FRAME } from "./stock-frame.generated.ts";
import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import YAML from "yaml";
import type { ContributionWindowColors } from "../api/types.ts";
import { createWindowPalette } from "./window-color-palette.ts";
import { contrastRatio, parseHexColor, type Rgba } from "./theme-color.ts";
import { gamutMappedColor, toOklch } from "./perceptual-color.ts";

function color(value: string): Rgba {
  const parsed = parseHexColor(value);
  assert(parsed, `Invalid fixture color ${value}`);
  return parsed;
}

type ThemeRecipe = { name: string; tokens: Record<string, string>; window_colors: ContributionWindowColors };
const packDirectory = resolve(import.meta.dirname, "../../../lycaon/config/packs/painted-wolf/themes/contributions/themes");
const themes = [...STOCK_FRAME.themes, ...readdirSync(packDirectory).filter(name => name.endsWith(".yaml"))
  .map(name => YAML.parse(readFileSync(resolve(packDirectory, name), "utf8")) as ThemeRecipe)];

describe("window color recipes", () => {
  it.each(themes)("keeps $name readable with stable window colors", theme => {
    const background = color(theme.tokens.background ?? ""), text = color(theme.tokens.text ?? "");
    const accent = color(theme.tokens["accent-signal"] ?? theme.tokens.accent ?? "");
    for (const increasedContrast of [false, true]) {
      const input = { background, text, accent, increasedContrast, recipe: theme.window_colors };
      const palette = createWindowPalette(input);
      const first = palette(1);
      const paints = Array.from({ length: 24 }, (_, i) => palette(i));
      for (const paint of [...paints, palette(10_000), palette(Number.MAX_SAFE_INTEGER)]) {
        expect(contrastRatio(color(paint.caret), background, background)).toBeGreaterThanOrEqual(increasedContrast ? 4.5 : 3);
        expect(contrastRatio(color(paint.labelText), color(paint.label), background)).toBeGreaterThanOrEqual(increasedContrast ? 7 : 4.5);
        expect(contrastRatio(text, color(paint.selection), background)).toBeGreaterThanOrEqual(Math.min(increasedContrast ? 7 : 4.5, contrastRatio(text, background, background)));
        expect(contrastRatio(text, color(paint.selectionStrong), background)).toBeGreaterThanOrEqual(Math.min(increasedContrast ? 7 : 4.5, contrastRatio(text, background, background)));
        expect(color(paint.selectionStrong).a).toBeGreaterThanOrEqual(color(paint.selection).a);
      }
      expect(new Set(paints.slice(0, 8).map(paint => paint.caret)).size).toBe(8);
      expect(palette(1)).toEqual(first);
      const reversed = createWindowPalette(input);
      expect(reversed(23)).toEqual(paints[23]);
      expect(reversed(1)).toEqual(first);
    }
  });

  it("round trips the sRGB primaries through OKLCH", () => {
    for (const hex of ["#ff0000", "#00ff00", "#0000ff", "#ffffff", "#000000", "#8fbcbb"]) {
      const expected = color(hex);
      const roundTrip = gamutMappedColor(toOklch(expected));
      expect(Math.abs(roundTrip.r - expected.r)).toBeLessThanOrEqual(1);
      expect(Math.abs(roundTrip.g - expected.g)).toBeLessThanOrEqual(1);
      expect(Math.abs(roundTrip.b - expected.b)).toBeLessThanOrEqual(1);
    }
  });

  it("honors a monochrome recipe without claiming distinct colors", () => {
    const palette = createWindowPalette({ recipe: { main: "#555555", anchors: ["#555555"], hue_spread: 0, chroma_min: 0, chroma_max: 0 },
      background: color("#ffffff"), text: color("#222222"), accent: color("#555555"), increasedContrast: false });
    const paint = color(palette(200).caret);
    expect(Math.abs(paint.r - paint.g)).toBeLessThanOrEqual(1);
    expect(Math.abs(paint.g - paint.b)).toBeLessThanOrEqual(1);
  });

  it("adjusts label surfaces when a mid-tone theme cannot provide increased contrast", () => {
    const background = color("#777777");
    const palette = createWindowPalette({ recipe: { main: "#887777", anchors: ["#778877"], hue_spread: 20, chroma_min: 0.04, chroma_max: 0.12 },
      background, text: color("#ffffff"), accent: color("#887777"), increasedContrast: true });
    for (const slot of [0, 1, 5, 50]) {
      const paint = palette(slot);
      expect(contrastRatio(color(paint.labelText), color(paint.label), background)).toBeGreaterThanOrEqual(7);
    }
  });
});
