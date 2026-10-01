/** Applies selected fonts through CSS variables. */
import {
  lookupBundledFont,
  resolveFontStack,
} from "./font-catalog.ts";
import type { DenFontRole } from "./font-catalog.generated.ts";

/** The custom property each role drives. */
export const FONT_ROLE_VARS: Readonly<Record<DenFontRole, string>> = {
  ui: "--den-font-ui",
  mono: "--den-font-mono",
};

/** Applies one font role through CSSOM. */
export function applyFontRole(role: DenFontRole, selection: string): void {
  if (typeof document === "undefined") return;
  document.documentElement.style.setProperty(
    FONT_ROLE_VARS[role],
    resolveFontStack(role, selection),
  );
}

export function applyFontSelections(selections: Record<DenFontRole, string>): void {
  applyFontRole("ui", selections.ui);
  applyFontRole("mono", selections.mono);
}

/** Preloads selected bundled font faces. */
export function warmFontSelections(selections: Record<DenFontRole, string>): void {
  if (typeof document === "undefined") return;
  const faces = document.fonts;
  if (typeof faces?.load !== "function") return;
  for (const role of ["ui", "mono"] as const) {
    const bundled = lookupBundledFont(role, selections[role]);
    if (!bundled) continue;
    void faces.load(`1rem "${bundled.family}"`).catch(() => undefined);
  }
}
