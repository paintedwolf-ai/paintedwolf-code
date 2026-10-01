/** Device font selections propagate through shared CSS properties. */
import { createSignal } from "solid-js";
import type { DenAppearancePrefs } from "../../../shared/app-state-types.ts";
import { getAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import {
  applyFontSelections,
  warmFontSelections,
} from "../../fonts/font-application.ts";
import { rememberBootFonts } from "../../fonts/boot-font-cache.ts";
import { resolveFontSelection } from "../../fonts/font-catalog.ts";
import type { DenFontRole } from "../../fonts/font-catalog.generated.ts";
import { patchAppearancePrefs } from "./appearance-state.ts";

const [uiFont, setUiFont] = createSignal(resolveFontSelection("ui", undefined));
const [monoFont, setMonoFont] = createSignal(resolveFontSelection("mono", undefined));

export function fontSelection(role: DenFontRole): string {
  return role === "mono" ? monoFont() : uiFont();
}

function selections(): Record<DenFontRole, string> {
  return { ui: uiFont(), mono: monoFont() };
}

export function resolveFontPrefs(
  prefs?: DenAppearancePrefs,
): Record<DenFontRole, string> {
  return {
    ui: resolveFontSelection("ui", prefs?.uiFont),
    mono: resolveFontSelection("mono", prefs?.monoFont),
  };
}

export function syncFontPrefsFromSnapshot(): void {
  const resolved = resolveFontPrefs(getAppStateSnapshot().appearance);
  setUiFont(resolved.ui);
  setMonoFont(resolved.mono);
  applyFonts();
}

/** Applies fonts and records the resulting boot paint. */
function applyFonts(): void {
  applyFontSelections(selections());
  warmFontSelections(selections());
  rememberBootFonts(selections());
}

async function persist(next: Record<DenFontRole, string>): Promise<void> {
  setUiFont(next.ui);
  setMonoFont(next.mono);
  applyFonts();
  await patchAppearancePrefs({
    uiFont: next.ui,
    monoFont: next.mono,
  });
}

export async function setFontSelection(
  role: DenFontRole,
  selection: string,
): Promise<void> {
  await persist({ ...selections(), [role]: selection });
}

/** Test-only preference reset. */
export function resetFontPrefsForTests(prefs?: DenAppearancePrefs): void {
  const resolved = resolveFontPrefs(prefs);
  setUiFont(resolved.ui);
  setMonoFont(resolved.mono);
}
