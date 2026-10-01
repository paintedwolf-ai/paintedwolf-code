/** Cached font stacks for immediate restoration during index.html boot. */
import { CONFIG_DIR_NAME } from "../../shared/brand.ts";
import { resolveFontStack } from "./font-catalog.ts";
import type { DenFontRole } from "./font-catalog.generated.ts";

/** LocalStorage key shared with the inline boot script in index.html. */
export const BOOT_FONT_STORAGE_KEY = `${CONFIG_DIR_NAME}.boot-fonts`;

export type BootFontMemo = {
  ui: string;
  mono: string;
};

/** Persists font stacks to localStorage for the boot script. */
export function rememberBootFonts(selections: Record<DenFontRole, string>): void {
  const memo: BootFontMemo = {
    ui: resolveFontStack("ui", selections.ui),
    mono: resolveFontStack("mono", selections.mono),
  };
  if (memo.ui.includes(";") || memo.mono.includes(";")) return;
  try {
    if (typeof localStorage === "undefined") return;
    localStorage.setItem(BOOT_FONT_STORAGE_KEY, JSON.stringify(memo));
  } catch {
    /* ignore */
  }
}

/** Clears cached boot font settings for testing. */
export function clearBootFontsForTests(): void {
  try {
    localStorage.removeItem(BOOT_FONT_STORAGE_KEY);
  } catch {
    /* ignore */
  }
}
