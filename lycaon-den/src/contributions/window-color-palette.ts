import { createPaletteReader } from "./palette-cache.ts";
import type { ContributionWindowColors } from "../api/types.ts";
import { gamutMappedColor, perceptualDistance, toOklab, toOklch } from "./perceptual-color.ts";
import { contrastRatio, formatHexColor, legibleInk, mixColors, parseHexColor, type Rgba } from "./theme-color.ts";

export const WINDOW_COLOR_VARS = {
  main: "--den-window-color-main",
  anchors: "--den-window-color-anchors",
  hue_spread: "--den-window-color-hue-spread",
  chroma_min: "--den-window-color-chroma-min",
  chroma_max: "--den-window-color-chroma-max",
} as const;

export function windowColorProperties(recipe: ContributionWindowColors): Record<string, string> {
  return Object.fromEntries(Object.entries(WINDOW_COLOR_VARS).map(([key, property]) => [property,
    key === "anchors" ? recipe.anchors.join(" ") : String(recipe[key as keyof typeof recipe])]));
}

export type WindowColor = { caret: string; selection: string; selectionStrong: string; label: string; labelText: string };
export type WindowPaletteInput = { recipe: ContributionWindowColors; background: Rgba; text: Rgba; accent: Rgba; increasedContrast: boolean };

function radicalInverse(value: number, base: number): number {
  let result = 0, weight = 1 / base;
  while (value > 0) {
    result += (value % base) * weight;
    value = Math.floor(value / base);
    weight /= base;
  }
  return result;
}

function labelPaint(color: Rgba, wash: Rgba, background: Rgba, minimum: number): Pick<WindowColor, "label" | "labelText"> {
  const ink = legibleInk(color, wash, background, minimum);
  let fill = wash;
  // Adjust the label surface when ink contrast is insufficient.
  const channel = ink.r + ink.g + ink.b > 382 ? 0 : 255;
  const pole = { r: channel, g: channel, b: channel, a: 255 };
  for (let percent = 1; percent <= 100 && contrastRatio(ink, fill, background) < minimum; percent++) {
    fill = mixColors(pole, percent, wash);
  }
  return { label: formatHexColor(fill), labelText: formatHexColor(ink) };
}

export type IdentityPaintInput = Pick<WindowPaletteInput, "background" | "text" | "increasedContrast">;

/** Caret, selection washes, and label paint for one identity color, held to display contrast. */
export function identityPaint(color: Rgba, input: IdentityPaintInput): WindowColor {
  const floor = Math.min(input.increasedContrast ? 7 : 4.5, contrastRatio(input.text, input.background, input.background));
  const selectionPaint = (opacity: number): Rgba => {
    let fill = { ...color, a: Math.round(opacity * 255 / 100) };
    for (let percent = opacity - 1; percent >= 0 && contrastRatio(input.text, fill, input.background) < floor; percent--) {
      fill = { ...color, a: Math.round(percent * 255 / 100) };
    }
    return fill;
  };
  const selection = selectionPaint(16);
  const wash = mixColors(color, selection.a * 100 / 255, input.background);
  return { caret: formatHexColor(color), selection: formatHexColor(selection), selectionStrong: formatHexColor(selectionPaint(28)),
    ...labelPaint(color, wash, input.background, input.increasedContrast ? 7 : 4.5) };
}

/** Window slots keep their colors as peers open and close. */
export function createWindowPalette(input: WindowPaletteInput): (slot: number) => WindowColor {
  const anchors = input.recipe.anchors.map(parseHexColor).filter((c): c is Rgba => c !== null);
  const seeds = (anchors.length ? anchors : [input.accent]).map(toOklch);
  const candidate = (index: number): Rgba => {
    const seed = seeds[index % seeds.length] ?? toOklch(input.accent);
    const cycle = Math.floor(index / seeds.length);
    const hue = seed.h + (cycle ? 2 * radicalInverse(cycle, 2) - 1 : 0) * input.recipe.hue_spread;
    const c = cycle ? input.recipe.chroma_min + radicalInverse(cycle, 3) * (input.recipe.chroma_max - input.recipe.chroma_min)
      : Math.max(input.recipe.chroma_min, Math.min(input.recipe.chroma_max, seed.c));
    const raw = gamutMappedColor({ l: Math.max(0.15, Math.min(0.85, seed.l)), c, h: hue });
    return legibleInk(raw, input.background, input.background, input.increasedContrast ? 4.5 : 3);
  };
  const main = legibleInk(parseHexColor(input.recipe.main) ?? input.accent, input.background, input.background, input.increasedContrast ? 4.5 : 3);
  const selected: Rgba[] = [main];
  const distances = [toOklab(input.accent), toOklab(main)];
  const candidates = Array.from({ length: 192 }, (_, index) => candidate(index));
  const labs = candidates.map(toOklab);
  const used = new Set<number>();
  const firstSlots = 48;
  function earlyColor(index: number): Rgba {
    while (selected.length <= index) {
      let best = 0, bestDistance = -1;
      for (let i = 0; i < candidates.length; i++) {
        const lab = labs[i];
        if (!lab || used.has(i)) continue;
        const distance = Math.min(...distances.map(previous => perceptualDistance(previous, lab)));
        if (distance > bestDistance) { best = i; bestDistance = distance; }
      }
      const color = candidates[best] ?? input.accent;
      used.add(best); selected.push(color); distances.push(toOklab(color));
    }
    return selected[index] ?? input.accent;
  }
  const paint = (color: Rgba): WindowColor => identityPaint(color, input);
  const cache = new Map<number, WindowColor>();
  return slot => {
    const index = Number.isSafeInteger(slot) && slot >= 0 ? slot : 0;
    const found = cache.get(index);
    if (found) return found;
    const result = paint(index < firstSlots ? earlyColor(index) : candidate(index + 192));
    // Bound cached paints across repeated window creation.
    if (cache.size >= 256) cache.clear();
    cache.set(index, result);
    return result;
  };
}

export const readWindowPalette = createPaletteReader((read, increasedContrast): ((slot: number) => WindowColor) | null => {
  const background = parseHexColor(read("--den-background"));
  const text = parseHexColor(read("--den-text"));
  const accent = parseHexColor(read("--den-accent-signal"));
  const anchors = read(WINDOW_COLOR_VARS.anchors).split(/\s+/).filter(v => parseHexColor(v));
  if (!background || !text || !accent || !anchors.length) return null;
  return createWindowPalette({ background, text, accent, increasedContrast,
    recipe: { main: read(WINDOW_COLOR_VARS.main), anchors, hue_spread: Number(read(WINDOW_COLOR_VARS.hue_spread)),
      chroma_min: Number(read(WINDOW_COLOR_VARS.chroma_min)), chroma_max: Number(read(WINDOW_COLOR_VARS.chroma_max)) } });
});

export function windowColorStyle(color: WindowColor | undefined): Record<string, string> {
  return { "--den-window-caret": color?.caret ?? "var(--den-accent-signal)",
    "--den-window-selection": color?.selection ?? "var(--den-selection)",
    "--den-window-label": color?.label ?? "var(--den-surface-elevated)",
    "--den-window-label-text": color?.labelText ?? "var(--den-text)" };
}
