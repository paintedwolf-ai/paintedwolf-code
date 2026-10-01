/** System display adjustments derive from the active theme. */
import {
  formatHexColor,
  legibleInk,
  mixColors,
  parseHexColor,
  readableOn,
  type Rgba,
} from "./theme-color.ts";

export type SystemThemeAdjustments = {
  /** A system accent in `#rrggbb`; null preserves the theme accent. */
  accent: string | null;
  increaseContrast: boolean;
};

export const NO_SYSTEM_ADJUSTMENTS: SystemThemeAdjustments = {
  accent: null,
  increaseContrast: false,
};

/** WCAG floors: body copy, enhanced copy, and non-text UI boundaries. */
const TEXT_FLOOR = 4.5;
const ENHANCED_TEXT_FLOOR = 7;
const BOUNDARY_FLOOR = 3;

type TokenValues = Readonly<Record<string, string>>;

function color(tokens: TokenValues, id: string): Rgba | null {
  const value = tokens[id];
  return value ? parseHexColor(value) : null;
}

/** A system accent updates the full accent family. */
function accentFamily(tokens: TokenValues, accent: Rgba): Record<string, string> {
  const background = color(tokens, "background");
  const shadow = color(tokens, "shadow");
  if (!background || !shadow) return {};
  const signal = legibleInk(accent, background, background, BOUNDARY_FLOOR);
  const hover = formatHexColor(mixColors(shadow, 12, accent));
  const onAccent = formatHexColor(readableOn(accent, background));
  return {
    accent: formatHexColor(accent),
    "accent-warm": formatHexColor(accent),
    "accent-hover": hover,
    "accent-warm-hover": hover,
    "on-accent": onAccent,
    "on-accent-warm": onAccent,
    "accent-text": formatHexColor(legibleInk(accent, background, background, TEXT_FLOOR)),
    "accent-signal": formatHexColor(signal),
    selection: formatHexColor(mixColors(signal, 10, background)),
    "selection-strong": formatHexColor(mixColors(signal, 32, background)),
  };
}

/** Raises secondary copy and boundaries to the enhanced floors. */
function contrastBoost(tokens: TokenValues): Record<string, string> {
  const background = color(tokens, "background");
  if (!background) return {};
  const out: Record<string, string> = {};
  const raise = (id: string, floor: number) => {
    const current = color(tokens, id);
    if (current) out[id] = formatHexColor(legibleInk(current, background, background, floor));
  };
  raise("text-muted", ENHANCED_TEXT_FLOOR);
  raise("accent-text", ENHANCED_TEXT_FLOOR);
  raise("border", BOUNDARY_FLOOR);
  return out;
}

export function adjustedTokens(
  tokens: TokenValues,
  adjustments: SystemThemeAdjustments,
): Record<string, string> {
  const accent = adjustments.accent ? parseHexColor(adjustments.accent) : null;
  const accentOverrides = accent ? accentFamily(tokens, accent) : {};
  const boosted = adjustments.increaseContrast
    ? contrastBoost({ ...tokens, ...accentOverrides })
    : {};
  return { ...accentOverrides, ...boosted };
}

/** Display adjustments invalidate cached thumbnails. */
export function adjustmentsKey(adjustments: SystemThemeAdjustments): string {
  return `${adjustments.accent ?? "theme"}|${adjustments.increaseContrast ? "more" : "standard"}`;
}
