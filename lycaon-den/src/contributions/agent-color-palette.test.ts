import { assert, describe, expect, it } from "vitest";
import { STOCK_FRAME } from "./stock-frame.generated.ts";
import { agentLightnessOffset, createAgentPalette } from "./agent-color-palette.ts";
import { contrastRatio, parseHexColor, type Rgba } from "./theme-color.ts";
import { toOklch } from "./perceptual-color.ts";

function color(value: string): Rgba {
  const parsed = parseHexColor(value);
  assert(parsed, `Invalid fixture color ${value}`);
  return parsed;
}

describe("agent color recipes", () => {
  it.each(STOCK_FRAME.themes)("keeps $name agent chats readable, muted, and apart", theme => {
    const background = color(theme.tokens.background ?? ""), text = color(theme.tokens.text ?? "");
    for (const increasedContrast of [false, true]) {
      const palette = createAgentPalette({ background, text, increasedContrast, recipe: theme.agent_colors });
      const paints = Array.from({ length: 12 }, (_, slot) => palette(slot));
      for (const paint of paints) {
        expect(contrastRatio(color(paint.caret), background, background)).toBeGreaterThanOrEqual(increasedContrast ? 4.5 : 3);
        expect(contrastRatio(color(paint.labelText), color(paint.label), background)).toBeGreaterThanOrEqual(increasedContrast ? 7 : 4.5);
        expect(toOklch(color(paint.caret)).c).toBeLessThanOrEqual(theme.agent_colors.chroma + 0.02);
      }
      if (!increasedContrast) expect(new Set(paints.slice(0, 3).map(paint => paint.caret)).size).toBe(3);
      expect(palette(2)).toEqual(paints[2]);
    }
  });

  it("alternates lighter and darker around the center", () => {
    expect([0, 1, 2, 3, 4].map(agentLightnessOffset)).toEqual([0, 1, -1, 2, -2]);
  });
});
