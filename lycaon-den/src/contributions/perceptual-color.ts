import type { Rgba } from "./theme-color.ts";

export type Oklab = { l: number; a: number; b: number };
export type Oklch = { l: number; c: number; h: number };

export function toOklab(color: Rgba): Oklab {
  const linear = (v: number) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  const r = linear(color.r / 255), g = linear(color.g / 255), b = linear(color.b / 255);
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return { l: 0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    a: 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    b: 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s };
}

export function toOklch(color: Rgba): Oklch {
  const { l, a, b } = toOklab(color);
  return { l, c: Math.hypot(a, b), h: Math.atan2(b, a) * 180 / Math.PI };
}

function linearChannels(color: Oklch): number[] {
  const a = color.c * Math.cos(color.h * Math.PI / 180);
  const b = color.c * Math.sin(color.h * Math.PI / 180);
  const l = (color.l + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (color.l - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (color.l - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s];
}

/** Reduce chroma into sRGB without changing the requested hue or lightness. */
export function gamutMappedColor(color: Oklch): Rgba {
  let low = 0, high = color.c;
  const inGamut = (channels: number[]) => channels.every(v => v >= -0.000001 && v <= 1.000001);
  let channels = linearChannels(color);
  if (!inGamut(channels)) {
    for (let step = 0; step < 20; step++) {
      const c = (low + high) / 2;
      if (inGamut(linearChannels({ ...color, c }))) low = c;
      else high = c;
    }
    channels = linearChannels({ ...color, c: low });
  }
  const encoded = channels.map(v => Math.round(255 * Math.max(0, Math.min(1,
    v <= 0.0031308 ? v * 12.92 : 1.055 * v ** (1 / 2.4) - 0.055))));
  return { r: encoded[0] ?? 0, g: encoded[1] ?? 0, b: encoded[2] ?? 0, a: 255 };
}

export function perceptualDistance(a: Oklab, b: Oklab): number {
  return Math.hypot(a.l - b.l, a.a - b.a, a.b - b.b);
}
