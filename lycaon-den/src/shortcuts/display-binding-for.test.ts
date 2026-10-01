import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import {
  ariaKeyShortcutsFor,
  displayBindingFor,
  formatChordsDisplay,
} from "./display-binding-for.ts";
import { setShortcutPlatformForTests } from "./platform.ts";
import { resetShortcutPrefsForTests } from "../settings/system/shortcut-prefs.ts";

beforeEach(() => {
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests("macos");
  seedStockFrame();
});

afterEach(() => {
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests(null);
  resetContributionStoreForTest();
});

describe("displayBindingFor", () => {
  it("formats the default search.open binding for the platform", () => {
    expect(displayBindingFor(stockId("search-open"))).toBe("⌘K");
    expect(
      displayBindingFor(stockId("search-open"), { platform: "linux", overrides: {} }),
    ).toBe("Ctrl+K");
  });

  it("joins multi-chord help.shortcuts with primary first", () => {
    expect(displayBindingFor(stockId("help-shortcuts"))).toBe("? / ⌘/");
  });

  it("formatChordsDisplay joins platform glyphs", () => {
    expect(formatChordsDisplay(["Mod+K"], "macos")).toBe("⌘K");
    expect(formatChordsDisplay(["?", "Mod+/"], "macos")).toBe("? / ⌘/");
    expect(formatChordsDisplay([], "macos")).toBe("—");
  });

  it("reflects an injected override", () => {
    expect(
      displayBindingFor(stockId("search-open"), {
        platform: "macos",
        overrides: { [stockBindingId("search-open")]: "Mod+J" },
      }),
    ).toBe("⌘J");
  });

  it("starts rename scope controls with native platform labels", () => {
    expect(
      displayBindingFor(stockId("editor-rename-scope-file"), {
        platform: "macos",
        overrides: null,
      }),
    ).toBe("⌥←");
    for (const platform of ["linux", "windows"] as const) {
      expect(
        displayBindingFor(stockId("editor-rename-scope-everywhere"), {
          platform,
          overrides: null,
        }),
      ).toBe("Alt+→");
    }
  });

  it("renders live bindings in WAI-ARIA modifier spelling", () => {
    expect(
      ariaKeyShortcutsFor(stockId("search-toggle-match-case"), {
        platform: "macos",
        overrides: {
          [stockBindingId("search-toggle-match-case")]: "Mod+Alt+J",
        },
      }),
    ).toBe("Meta+Alt+J");
    expect(
      ariaKeyShortcutsFor(stockId("search-toggle-match-case"), {
        platform: "linux",
        overrides: {
          [stockBindingId("search-toggle-match-case")]: "Mod+Shift+J",
        },
      }),
    ).toBe("Control+Shift+J");
  });
});
