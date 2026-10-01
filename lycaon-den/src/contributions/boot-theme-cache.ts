/** Cold-start appearance replay. */
import { CONFIG_DIR_NAME } from "../../shared/brand.ts";
import type { ContributionTheme } from "../api/types.ts";
import {
  themeCustomProperties,
  type AppearanceMode,
} from "./theme-application.ts";

/** Storage key shared with the inline boot script. */
export const BOOT_THEME_STORAGE_KEY = `${CONFIG_DIR_NAME}.boot-theme`;

/** One scheme's last paint: a declaration block plus the logomark it stamped. */
type BootThemePaint = {
  style: string;
  logomark: string;
};

type BootThemeMemo = {
  mode: AppearanceMode;
  light?: BootThemePaint;
  dark?: BootThemePaint;
};

function writeMemo(memo: BootThemeMemo): void {
  try {
    if (typeof localStorage === "undefined") return;
    localStorage.setItem(BOOT_THEME_STORAGE_KEY, JSON.stringify(memo));
  } catch {
    /* Persistence is best-effort. */
  }
}

/** Builds the semicolon-delimited boot declaration block. */
function declarationBlock(theme: ContributionTheme): string {
  return Object.entries(themeCustomProperties(theme))
    .filter(([, value]) => !value.includes(";"))
    .map(([cssVar, value]) => `${cssVar}:${value};`)
    .join("");
}

/** Replaces the complete cold-start palette. A null entry falls back to stock. */
export function rememberBootPalette(
  mode: AppearanceMode,
  themes: Readonly<{
    light: ContributionTheme | null;
    dark: ContributionTheme | null;
  }>,
): void {
  const memo: BootThemeMemo = { mode };
  for (const scheme of ["light", "dark"] as const) {
    const theme = themes[scheme];
    if (!theme) continue;
    const paint: BootThemePaint = {
      style: declarationBlock(theme),
      logomark: theme.logomark,
    };
    memo[scheme] = paint;
  }
  writeMemo(memo);
}

/** Test-only boot memo reset. */
export function clearBootPaletteForTests(): void {
  try {
    localStorage.removeItem(BOOT_THEME_STORAGE_KEY);
  } catch {
    /* Storage is unavailable. */
  }
}
