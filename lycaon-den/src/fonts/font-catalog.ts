/** Font selection resolution, validation, and CSS stack construction. */
import {
  DEN_BUNDLED_FONTS,
  DEN_FONT_DEFAULTS,
  DEN_FONT_FALLBACKS,
  type DenBundledFont,
  type DenFontRole,
} from "./font-catalog.generated.ts";

/** Selection sentinel: whatever this platform calls its interface or mono face. */
export const SYSTEM_FONT = "system";

/** Longest family name accepted from a stored preference or the custom field. */
export const FONT_FAMILY_MAX_LENGTH = 64;

/** Allowed characters for sanitized CSS font-family names. */
const FAMILY_ALLOWED = /^[\p{L}\p{N} ._-]+$/u;

export function bundledFontsInRole(role: DenFontRole): DenBundledFont[] {
  return DEN_BUNDLED_FONTS.filter((f) => f.role === role);
}

export function lookupBundledFont(
  role: DenFontRole,
  family: string,
): DenBundledFont | undefined {
  return DEN_BUNDLED_FONTS.find((f) => f.role === role && f.family === family);
}

/** Normalizes a family name, or returns null if invalid. */
export function sanitizeFontFamily(raw: string): string | null {
  const family = raw.trim().replace(/\s+/g, " ");
  if (!family || family.length > FONT_FAMILY_MAX_LENGTH) return null;
  if (!FAMILY_ALLOWED.test(family)) return null;
  return family;
}

/** Builds the CSS font stack for a font selection. */
export function resolveFontStack(role: DenFontRole, selection: string): string {
  const fallback = DEN_FONT_FALLBACKS[role];
  if (selection === SYSTEM_FONT) return fallback;

  const bundled = lookupBundledFont(role, selection);
  if (bundled) return bundled.stack;

  const custom = sanitizeFontFamily(selection);
  if (!custom) return fallback;
  return `"${custom}", ${fallback}`;
}

/** Resolves the active font selection from stored preferences or product defaults. */
export function resolveFontSelection(
  role: DenFontRole,
  stored: string | undefined,
): string {
  if (stored === SYSTEM_FONT) return SYSTEM_FONT;
  const family = typeof stored === "string" ? sanitizeFontFamily(stored) : null;
  return family ?? DEN_FONT_DEFAULTS[role];
}

/** Whether a selection is guaranteed to render — bundled or the platform's own. */
export function isGuaranteedFont(role: DenFontRole, selection: string): boolean {
  return selection === SYSTEM_FONT || lookupBundledFont(role, selection) !== undefined;
}
