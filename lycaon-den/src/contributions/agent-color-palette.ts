import { createPaletteReader } from "./palette-cache.ts";
import type { ContributionAgentColors } from "../api/types.ts";
import { gamutMappedColor, toOklch } from "./perceptual-color.ts";
import { legibleInk, parseHexColor, type Rgba } from "./theme-color.ts";
import { identityPaint, type WindowColor } from "./window-color-palette.ts";

export const AGENT_COLOR_VARS = {
  main: "--den-agent-color-main",
  lightness_step: "--den-agent-color-lightness-step",
  chroma: "--den-agent-color-chroma",
} as const;

export function agentColorProperties(recipe: ContributionAgentColors): Record<string, string> {
  return {
    [AGENT_COLOR_VARS.main]: recipe.main,
    [AGENT_COLOR_VARS.lightness_step]: String(recipe.lightness_step),
    [AGENT_COLOR_VARS.chroma]: String(recipe.chroma),
  };
}

export type AgentPaletteInput = {
  recipe: ContributionAgentColors;
  background: Rgba;
  text: Rgba;
  increasedContrast: boolean;
};

/** Lightness offsets in steps for slot 0, 1, 2…: center, lighter, darker, then further out. */
export function agentLightnessOffset(slot: number): number {
  if (slot <= 0) return 0;
  const distance = Math.ceil(slot / 2);
  return slot % 2 === 1 ? distance : -distance;
}

/** Every chat shares one muted hue; a slot is a lightness step from the recipe's main color. */
export function createAgentPalette(input: AgentPaletteInput): (slot: number) => WindowColor {
  const main = toOklch(parseHexColor(input.recipe.main) ?? input.text);
  const cache = new Map<number, WindowColor>();
  return slot => {
    const index = Number.isSafeInteger(slot) && slot >= 0 ? slot : 0;
    const found = cache.get(index);
    if (found) return found;
    // Wrap far slots back through the band so lightness stays inside what the display can separate.
    const offset = agentLightnessOffset(index % 7);
    const l = Math.max(0.2, Math.min(0.9, main.l + offset * input.recipe.lightness_step));
    const raw = gamutMappedColor({ l, c: input.recipe.chroma, h: main.h });
    const color = legibleInk(raw, input.background, input.background, input.increasedContrast ? 4.5 : 3);
    const paint = identityPaint(color, input);
    if (cache.size >= 64) cache.clear();
    cache.set(index, paint);
    return paint;
  };
}

export const readAgentPalette = createPaletteReader((read, increasedContrast): ((slot: number) => WindowColor) | null => {
  const background = parseHexColor(read("--den-background"));
  const text = parseHexColor(read("--den-text"));
  const main = read(AGENT_COLOR_VARS.main);
  const step = Number(read(AGENT_COLOR_VARS.lightness_step));
  const chroma = Number(read(AGENT_COLOR_VARS.chroma));
  if (!background || !text || !parseHexColor(main) || !Number.isFinite(step) || !Number.isFinite(chroma)) return null;
  return createAgentPalette({ background, text,
    increasedContrast,
    recipe: { main, lightness_step: step, chroma } });
});

/** CSS custom properties that paint one agent chat. */
export function agentColorStyle(color: WindowColor | undefined): Record<string, string> {
  return { "--den-agent-caret": color?.caret ?? "var(--den-text-muted)",
    "--den-agent-selection": color?.selection ?? "var(--den-selection)",
    "--den-agent-label": color?.label ?? "var(--den-surface-elevated)",
    "--den-agent-label-text": color?.labelText ?? "var(--den-text)" };
}
