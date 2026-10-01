// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import {
  BOOT_FONT_STORAGE_KEY,
  clearBootFontsForTests,
} from "../../fonts/boot-font-cache.ts";
import { FONT_ROLE_VARS } from "../../fonts/font-application.ts";
import { SYSTEM_FONT } from "../../fonts/font-catalog.ts";
import { DEN_FONT_DEFAULTS, DEN_FONT_FALLBACKS } from "../../fonts/font-catalog.generated.ts";
import {
  fontSelection,
  resetFontPrefsForTests,
  resolveFontPrefs,
  setFontSelection,
  syncFontPrefsFromSnapshot,
} from "./font-prefs.ts";

beforeEach(() => {
  resetAppStateSnapshotForTests();
  resetFontPrefsForTests();
  clearBootFontsForTests();
});

afterEach(() => {
  document.documentElement.style.removeProperty(FONT_ROLE_VARS.ui);
  document.documentElement.style.removeProperty(FONT_ROLE_VARS.mono);
});

describe("resolveFontPrefs", () => {
  it("starts on the product defaults", () => {
    expect(resolveFontPrefs(undefined)).toEqual({
      ui: DEN_FONT_DEFAULTS.ui,
      mono: DEN_FONT_DEFAULTS.mono,
    });
  });

  it("reads each role independently", () => {
    expect(resolveFontPrefs({ uiFont: SYSTEM_FONT })).toEqual({
      ui: SYSTEM_FONT,
      mono: DEN_FONT_DEFAULTS.mono,
    });
  });
});

describe("font preferences", () => {
  it("applies the stored selection on sync", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { uiFont: "Literata", monoFont: SYSTEM_FONT },
    });

    syncFontPrefsFromSnapshot();

    expect(fontSelection("ui")).toBe("Literata");
    expect(fontSelection("mono")).toBe(SYSTEM_FONT);
    expect(
      document.documentElement.style.getPropertyValue(FONT_ROLE_VARS.ui),
    ).toContain('"Literata"');
    expect(
      document.documentElement.style.getPropertyValue(FONT_ROLE_VARS.mono),
    ).toBe(DEN_FONT_FALLBACKS.mono);
  });

  it("persists one role without disturbing the other", async () => {
    await setFontSelection("ui", "DM Sans");

    expect(getAppStateSnapshot()).toEqual(expect.objectContaining({
      appearance: expect.objectContaining({
        uiFont: "DM Sans",
        monoFont: DEN_FONT_DEFAULTS.mono,
      }),
    }));
    expect(fontSelection("mono")).toBe(DEN_FONT_DEFAULTS.mono);
  });

  it("keeps the theme selection when a font changes", async () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "dark", darkTheme: "acme/kit:nightshade" },
    });
    syncFontPrefsFromSnapshot();

    await setFontSelection("mono", SYSTEM_FONT);

    expect(getAppStateSnapshot()).toEqual(expect.objectContaining({
      appearance: expect.objectContaining({
        mode: "dark",
        darkTheme: "acme/kit:nightshade",
        monoFont: SYSTEM_FONT,
      }),
    }));
  });

  it("memos what it painted so the next boot does not reflow", async () => {
    await setFontSelection("ui", "Space Grotesk");

    const memo = JSON.parse(localStorage.getItem(BOOT_FONT_STORAGE_KEY)!) as {
      ui: string;
    };
    expect(memo.ui).toContain('"Space Grotesk"');
  });
});
