/** Matches the sRGB derivation in lycaon/internal/theme/color.go. */

export type Rgba = { r: number; g: number; b: number; a: number };

const WHITE: Rgba = { r: 255, g: 255, b: 255, a: 255 };
const BLACK: Rgba = { r: 0, g: 0, b: 0, a: 255 };
const DARK_INK: Rgba = { r: 0x14, g: 0x13, b: 0x12, a: 255 };
const LEGIBLE_INK_STEPS = 24;

export function parseHexColor(value: string): Rgba | null {
  const hex = value.trim().replace(/^#/, "");
  if (!/^([0-9a-f]{6}|[0-9a-f]{8})$/i.test(hex)) return null;
  const channel = (at: number) => parseInt(hex.slice(at, at + 2), 16);
  return {
    r: channel(0),
    g: channel(2),
    b: channel(4),
    a: hex.length === 8 ? channel(6) : 255,
  };
}

export function formatHexColor(color: Rgba): string {
  const hex = (value: number) => value.toString(16).padStart(2, "0");
  const rgb = `#${hex(color.r)}${hex(color.g)}${hex(color.b)}`;
  return color.a === 255 ? rgb : `${rgb}${hex(color.a)}`;
}

function clamp01(value: number): number {
  return Math.min(1, Math.max(0, value));
}

function premultipliedChannel(
  a: number,
  aw: number,
  b: number,
  bw: number,
  alpha: number,
): number {
  return Math.round((a * aw + b * bw) / alpha);
}

/** CSS `color-mix(in srgb, a pct%, b)`. */
export function mixColors(a: Rgba, pct: number, b: Rgba): Rgba {
  const w = clamp01(pct / 100);
  const aw = (w * a.a) / 255;
  const bw = ((1 - w) * b.a) / 255;
  const alpha = aw + bw;
  if (alpha === 0) return { r: 0, g: 0, b: 0, a: 0 };
  return {
    r: premultipliedChannel(a.r, aw, b.r, bw, alpha),
    g: premultipliedChannel(a.g, aw, b.g, bw, alpha),
    b: premultipliedChannel(a.b, aw, b.b, bw, alpha),
    a: Math.round(alpha * 255),
  };
}

function compositeOver(fg: Rgba, bg: Rgba): Rgba {
  const fa = fg.a / 255;
  const ba = bg.a / 255;
  const alpha = fa + ba * (1 - fa);
  if (alpha === 0) return { r: 0, g: 0, b: 0, a: 0 };
  const bw = ba * (1 - fa);
  return {
    r: premultipliedChannel(fg.r, fa, bg.r, bw, alpha),
    g: premultipliedChannel(fg.g, fa, bg.g, bw, alpha),
    b: premultipliedChannel(fg.b, fa, bg.b, bw, alpha),
    a: Math.round(alpha * 255),
  };
}

function flatten(fg: Rgba, backdrop: Rgba): Rgba {
  return compositeOver(fg, { ...backdrop, a: 255 });
}

function relativeLuminance(color: Rgba): number {
  const lin = (value: number) => {
    const s = value / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(color.r) + 0.7152 * lin(color.g) + 0.0722 * lin(color.b);
}

/** WCAG 2.1 contrast of `fg` over `bg`, both composited on `backdrop`. */
export function contrastRatio(fg: Rgba, bg: Rgba, backdrop: Rgba): number {
  const renderedBg = flatten(bg, backdrop);
  const renderedFg = compositeOver(fg, renderedBg);
  const l1 = relativeLuminance(renderedFg);
  const l2 = relativeLuminance(renderedBg);
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}

/** Mixes toward black or white only as far as the contrast floor requires. */
export function legibleInk(hue: Rgba, bg: Rgba, backdrop: Rgba, min: number): Rgba {
  if (contrastRatio(hue, bg, backdrop) >= min) return hue;
  const pole = relativeLuminance(flatten(bg, backdrop)) > 0.18 ? BLACK : WHITE;
  if (contrastRatio(pole, bg, backdrop) < min) return pole;
  let lo = 0;
  let hi = 1;
  for (let i = 0; i < LEGIBLE_INK_STEPS; i++) {
    const mid = (lo + hi) / 2;
    if (contrastRatio(mixColors(pole, mid * 100, hue), bg, backdrop) >= min) {
      hi = mid;
    } else {
      lo = mid;
    }
  }
  return mixColors(pole, hi * 100, hue);
}

/** The light or dark label ink that reads better on `fill`. */
export function readableOn(fill: Rgba, backdrop: Rgba): Rgba {
  return contrastRatio(WHITE, fill, backdrop) >= contrastRatio(DARK_INK, fill, backdrop)
    ? WHITE
    : DARK_INK;
}
