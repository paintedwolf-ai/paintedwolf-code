/** Device-scope appearance. */
import { createSignal } from "solid-js";
import type { DenAppearancePrefs } from "../../../shared/app-state-types.ts";
import type { ContributionTheme } from "../../api/types.ts";
import { getAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import {
  applyAppearance,
  applyTheme,
  clearTheme,
  DEFAULT_APPEARANCE,
  resolveActiveTheme,
  resolvedScheme,
  type AppearanceMode,
  type AppearanceSelection,
  themePaintKey,
} from "../../contributions/theme-application.ts";
import { rememberBootPalette } from "../../contributions/boot-theme-cache.ts";
import {
  presentedContributionThemes,
  type ContributionFrameState,
} from "../../contributions/contribution-store.ts";
import { syncWindowBackdrop } from "../../platform/windows/window-backdrop.ts";
import {
  onSystemAppearanceChange,
  systemThemeAdjustments,
} from "../../platform/desktop/system-appearance.ts";
import { adjustmentsKey } from "../../contributions/theme-adjustments.ts";
import {
  onOsColorSchemeChange,
  osColorSchemeIsDark,
} from "../../theme.ts";
import { patchAppearancePrefs } from "./appearance-state.ts";

const [selection, setSelection] =
  createSignal<AppearanceSelection>(DEFAULT_APPEARANCE);
const [activePaintKey, setActivePaintKey] = createSignal<string | null>(null);
const paintListeners = new Set<(paintKey: string) => void>();

/** The live selection — stored preferences over the stock defaults. */
export function appearanceSelection(): AppearanceSelection {
  return selection();
}

function resolveAppearanceSelection(
  prefs?: DenAppearancePrefs,
): AppearanceSelection {
  return {
    mode: prefs?.mode ?? DEFAULT_APPEARANCE.mode,
    lightTheme: prefs?.lightTheme ?? DEFAULT_APPEARANCE.lightTheme,
    darkTheme: prefs?.darkTheme ?? DEFAULT_APPEARANCE.darkTheme,
  };
}

export function syncAppearanceFromSnapshot(): void {
  setSelection(resolveAppearanceSelection(getAppStateSnapshot().appearance));
  applyActiveTheme();
}

/** Scheme follows stored mode and the OS; theme values wait for a hydrated frame. */
export function startAppearanceWatch(): () => void {
  applyAppearance(activeScheme());
  const stopScheme = onOsColorSchemeChange(() => {
    if (selection().mode !== "system") return;
    applyActiveTheme();
  });
  const stopSystem = onSystemAppearanceChange(applyActiveTheme);
  return () => {
    stopScheme();
    stopSystem();
  };
}

export function applyActiveTheme(): void {
  const themes = presentedContributionThemes();
  if (!themes) {
    applyAppearance(activeScheme());
    return;
  }
  applyThemeCollection(themes);
}

function applyThemeCollection(themes: readonly ContributionTheme[]): void {
  const scheme = activeScheme();
  const resolution = resolveActiveTheme(selection(), themes, scheme);
  if (!resolution.theme) {
    resetActiveTheme();
    return;
  }
  const adjustments = systemThemeAdjustments();
  applyTheme(resolution.theme, undefined, adjustments);
  syncColdStartPalette(themes, scheme);
  publishPaint(`${themePaintKey(resolution.theme)}|${adjustmentsKey(adjustments)}`);
}

/** Commits theme paint at the contribution frame's readiness boundary. */
export function applyContributionThemeFrame(state: ContributionFrameState): void {
  if (state.phase === "unavailable") {
    resetActiveTheme();
    return;
  }
  applyThemeCollection(state.frame.themes ?? []);
}

function resetActiveTheme(): void {
  const scheme = activeScheme();
  clearTheme();
  applyAppearance(scheme);
  rememberBootPalette(selection().mode, { light: null, dark: null });
  syncWindowBackdrop(selection().mode, scheme, {});
  publishPaint(`stock:${scheme}`);
}

function themeForScheme(
  themes: readonly ContributionTheme[],
  scheme: "light" | "dark",
): ContributionTheme | null {
  return resolveActiveTheme(selection(), themes, scheme).theme;
}

function syncColdStartPalette(
  themes: readonly ContributionTheme[],
  scheme: "light" | "dark",
): void {
  const light = themeForScheme(themes, "light");
  const dark = themeForScheme(themes, "dark");
  rememberBootPalette(selection().mode, { light, dark });
  syncWindowBackdrop(selection().mode, scheme, {
    light: light?.tokens.background,
    dark: dark?.tokens.background,
  });
}

function publishPaint(paintKey: string): void {
  if (paintKey === activePaintKey()) return;
  setActivePaintKey(paintKey);
  for (const listener of paintListeners) listener(paintKey);
}

export function activeThemePaintKey(): string | null {
  return activePaintKey();
}

export function onActiveThemeChange(
  listener: (paintKey: string) => void,
): () => void {
  paintListeners.add(listener);
  return () => paintListeners.delete(listener);
}

async function persist(next: AppearanceSelection): Promise<void> {
  setSelection(next);
  applyActiveTheme();
  await patchAppearancePrefs({
    mode: next.mode,
    lightTheme: next.lightTheme,
    darkTheme: next.darkTheme,
  });
}

export async function setAppearanceMode(mode: AppearanceMode): Promise<void> {
  await persist({ ...selection(), mode });
}

/** Sets the slot for the theme's own appearance, not the active mode. */
export async function setAppearanceTheme(
  themeId: string,
  appearance: "light" | "dark",
): Promise<void> {
  const next = { ...selection() };
  if (appearance === "dark") next.darkTheme = themeId;
  else next.lightTheme = themeId;
  await persist(next);
}

/** The scheme the current mode resolves to. */
export function activeScheme(): "light" | "dark" {
  return resolvedScheme(selection().mode, osColorSchemeIsDark());
}

/** Test-only preference reset. */
export function resetAppearancePrefsForTests(): void {
  setSelection(DEFAULT_APPEARANCE);
  setActivePaintKey(null);
  paintListeners.clear();
}
