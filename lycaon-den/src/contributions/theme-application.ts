import { AGENT_COLOR_VARS, agentColorProperties } from "./agent-color-palette.ts";
import { WINDOW_COLOR_VARS, windowColorProperties } from "./window-color-palette.ts";
import type { ContributionTheme } from "../api/types.ts";
import { THEME_TOKEN_VARS, SYNTAX_SCOPE_VARS } from "./theme-vocabulary.generated.ts";
import {
  iconStrokeProperties,
  setThemeGlyphs,
  THEME_ICON_STROKE_VARS,
} from "./theme-icons.ts";
import {
  adjustedTokens,
  NO_SYSTEM_ADJUSTMENTS,
  type SystemThemeAdjustments,
} from "./theme-adjustments.ts";

export type AppearanceMode = "light" | "dark" | "system";

export type AppearanceSelection = {
  mode: AppearanceMode;
  lightTheme: string;
  darkTheme: string;
};

export const STOCK_LIGHT_THEME = "painted-wolf/platform:daylight";
export const STOCK_DARK_THEME = "painted-wolf/platform:charcoal";

export const DEFAULT_APPEARANCE: AppearanceSelection = {
  mode: "system",
  lightTheme: STOCK_LIGHT_THEME,
  darkTheme: STOCK_DARK_THEME,
};

export function resolvedScheme(
  mode: AppearanceMode,
  osDark: boolean,
): "light" | "dark" {
  if (mode !== "system") return mode;
  return osDark ? "dark" : "light";
}

export type ThemeResolution = {
  theme: ContributionTheme | null;
  selectedId: string;
};

/** Unavailable themes fall back to the scheme's stock theme. */
export function resolveActiveTheme(
  selection: AppearanceSelection,
  themes: readonly ContributionTheme[],
  scheme: "light" | "dark",
): ThemeResolution {
  const selectedId =
    scheme === "dark" ? selection.darkTheme : selection.lightTheme;
  const stockId = scheme === "dark" ? STOCK_DARK_THEME : STOCK_LIGHT_THEME;

  const selected = themes.find(
    (t) => t.id === selectedId && t.appearance === scheme,
  );
  if (selected) return { theme: selected, selectedId };

  const stock = themes.find((t) => t.id === stockId);
  return { theme: stock ?? null, selectedId };
}

/** Appearance is available before theme values load. */
export function applyAppearance(
  scheme: "light" | "dark",
  root?: HTMLElement,
): void {
  const el = root ?? document.documentElement;
  el.setAttribute("data-den-appearance", scheme);
  el.style.colorScheme = scheme;
}

export function themeCustomProperties(
  theme: ContributionTheme,
): Record<string, string> {
  const props: Record<string, string> = {};
  for (const [id, cssVar] of Object.entries(THEME_TOKEN_VARS)) {
    const value = theme.tokens[id];
    if (value) props[cssVar] = value;
  }
  for (const [id, cssVar] of Object.entries(SYNTAX_SCOPE_VARS)) {
    const entry = theme.syntax[id];
    if (!entry) continue;
    props[cssVar] = entry.color;
    // Explicit defaults keep typography from inheriting across syntax scopes.
    props[`${cssVar}-style`] = entry.italic ? "italic" : "normal";
    props[`${cssVar}-weight`] = entry.bold ? "600" : "inherit";
    props[`${cssVar}-decoration`] = entry.underline ? "underline" : "none";
  }
  return { ...props, ...iconStrokeProperties(theme.icon_stroke), ...windowColorProperties(theme.window_colors),
    ...agentColorProperties(theme.agent_colors) };
}

export function adjustedThemeProperties(
  theme: ContributionTheme,
  adjustments: SystemThemeAdjustments,
): Record<string, string> {
  const props = themeCustomProperties(theme);
  const tokenVars: Readonly<Record<string, string>> = THEME_TOKEN_VARS;
  for (const [id, value] of Object.entries(adjustedTokens(theme.tokens, adjustments))) {
    const cssVar = tokenVars[id];
    if (cssVar) props[cssVar] = value;
  }
  return props;
}

export function applyTheme(
  theme: ContributionTheme,
  root?: HTMLElement,
  adjustments: SystemThemeAdjustments = NO_SYSTEM_ADJUSTMENTS,
): void {
  const el = root ?? document.documentElement;
  removeBootThemeReplay(el.ownerDocument);
  for (const [cssVar, value] of Object.entries(adjustedThemeProperties(theme, adjustments))) {
    el.style.setProperty(cssVar, value);
  }
  // Visibility preserves the logo's layout box.
  el.setAttribute("data-den-logomark", theme.logomark);
  // Replacement clears omitted glyph overrides.
  setThemeGlyphs(theme.icons);
  applyAppearance(theme.appearance as "light" | "dark", el);
}

/** Clearing inline overrides exposes the generated stock theme. */
export function clearTheme(root?: HTMLElement): void {
  const el = root ?? document.documentElement;
  removeBootThemeReplay(el.ownerDocument);
  for (const cssVar of Object.values(THEME_TOKEN_VARS)) {
    el.style.removeProperty(cssVar);
  }
  for (const cssVar of Object.values(SYNTAX_SCOPE_VARS)) {
    el.style.removeProperty(cssVar);
    el.style.removeProperty(`${cssVar}-style`);
    el.style.removeProperty(`${cssVar}-weight`);
    el.style.removeProperty(`${cssVar}-decoration`);
  }
  for (const cssVar of [...THEME_ICON_STROKE_VARS, ...Object.values(WINDOW_COLOR_VARS), ...Object.values(AGENT_COLOR_VARS)]) {
    el.style.removeProperty(cssVar);
  }
  el.removeAttribute("data-den-logomark");
  setThemeGlyphs(undefined);
}

function removeBootThemeReplay(doc: Document): void {
  doc.getElementById("den-boot-theme-replay")?.remove();
}

function canonicalThemeValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalThemeValue);
  if (value === null || typeof value !== "object") return value;
  return Object.fromEntries(
    Object.entries(value as Record<string, unknown>)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([key, entry]) => [key, canonicalThemeValue(entry)]),
  );
}

/** Every thumbnail input contributes to its cache key. */
export function themePaintKey(theme: ContributionTheme): string {
  return JSON.stringify(
    canonicalThemeValue({
      appearance: theme.appearance,
      properties: themeCustomProperties(theme),
      logomark: theme.logomark,
      icons: theme.icons ?? {},
    }),
  );
}
