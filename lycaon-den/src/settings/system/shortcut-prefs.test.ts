// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  clearShortcutOverride,
  resetAllShortcuts,
  resetShortcutPrefsForTests,
  saveShortcutOverride,
  shortcutOverrides,
  syncShortcutPrefsFromSnapshot,
} from "./shortcut-prefs.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import {
  getAppStateSnapshot,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { resetDispatcherForTests } from "../../shortcuts/dispatcher.ts";
import { resolveKeymap } from "../../shortcuts/keymap.ts";
import { liveKeymapSource } from "../../contributions/frame-keymap.ts";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";

describe("shortcut-prefs", () => {
  beforeEach(() => {
    // Expectations use macOS reservations whatever the test host is.
    setShortcutPlatformForTests("macos");
    localStorage.clear();
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
    resetDispatcherForTests();
    resetShortcutPrefsForTests();
    seedStockFrame();
  });

  afterEach(() => {
    setShortcutPlatformForTests(null);
    resetContributionStoreForTest();
  });

  it("defaults to an empty override map", () => {
    expect(shortcutOverrides()).toEqual({});
  });

  it("persists an override and feeds the dispatcher resolver", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    expect(shortcutOverrides()).toEqual({ [stockBindingId("search-open")]: "Mod+J" });
    expect(getAppStateSnapshot().shortcuts?.overrides).toEqual({
      [stockBindingId("search-open")]: "Mod+J",
    });
    const loaded = await loadAppState();
    expect(loaded.shortcuts?.overrides?.[stockBindingId("search-open")]).toBe("Mod+J");
    expect(
      resolveKeymap(shortcutOverrides(), liveKeymapSource("macos")).byCommand.get(stockId("search-open")),
    ).toEqual(["Mod+J"]);
  });

  it("normalizes override modifier order on save", async () => {
    await saveShortcutOverride(stockBindingId("nav-toggle"), "Shift+Mod+B");
    expect(shortcutOverrides()[stockBindingId("nav-toggle")]).toBe("Mod+Shift+B");
  });

  it("clears one override without touching others", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    await saveShortcutOverride(stockBindingId("session-new"), "Mod+Shift+B");
    await clearShortcutOverride(stockBindingId("search-open"));
    expect(shortcutOverrides()).toEqual({ [stockBindingId("session-new")]: "Mod+Shift+B" });
    expect(getAppStateSnapshot().shortcuts?.overrides).toEqual({
      [stockBindingId("session-new")]: "Mod+Shift+B",
    });
  });

  it("reset-all clears every override", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
    await saveShortcutOverride(stockBindingId("launcher-open"), "Mod+Shift+P");
    await resetAllShortcuts();
    expect(shortcutOverrides()).toEqual({});
    expect(getAppStateSnapshot().shortcuts).toBeUndefined();
  });

  it("syncShortcutPrefsFromSnapshot restores overrides into the signal", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      shortcuts: { overrides: { [stockBindingId("settings-open")]: "Mod+Shift+," } },
    });
    syncShortcutPrefsFromSnapshot();
    expect(shortcutOverrides()).toEqual({ [stockBindingId("settings-open")]: "Mod+Shift+," });
  });

  it("drops stale command-level keys and unknown declaration ids on load", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      shortcuts: {
        overrides: {
          [stockId("search-open")]: "Mod+J",
          "acme/removed:key-search": "Mod+L",
        },
      },
    });
    syncShortcutPrefsFromSnapshot();
    expect(shortcutOverrides()).toEqual({});
  });

  it("drops reserved and platform-unproducible bindings on load", () => {
    setShortcutPlatformForTests("linux");
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      shortcuts: {
        overrides: {
          [stockBindingId("search-open")]: "Mod+Alt+J",
          [stockBindingId("settings-open")]: "Ctrl+Tab",
        },
      },
    });
    syncShortcutPrefsFromSnapshot();
    expect(shortcutOverrides()).toEqual({});
  });

  it("drops every custom participant in a loaded same-scope conflict", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      shortcuts: {
        overrides: {
          [stockBindingId("search-open")]: "Mod+Shift+B",
          [stockBindingId("session-new")]: "Mod+Shift+B",
        },
      },
    });
    syncShortcutPrefsFromSnapshot();
    expect(shortcutOverrides()).toEqual({});
  });

  it("saves nav.leader and rewrites matching child sequences", async () => {
    await saveShortcutOverride("nav.leader", "Mod+;");
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+; D");
    expect(shortcutOverrides()).toEqual({
      "nav.leader": "Mod+;",
      [stockBindingId("search-open")]: "Mod+; D",
    });
    await saveShortcutOverride("nav.leader", "Mod+'");
    expect(shortcutOverrides()["nav.leader"]).toBe("Mod+'");
    expect(shortcutOverrides()[stockBindingId("search-open")]).toBe("Mod+' D");
  });

  it("rebinding the default leader preserves custom child sequences", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+; D");
    await saveShortcutOverride("nav.leader", "Mod+'");
    expect(shortcutOverrides()).toEqual({
      "nav.leader": "Mod+'",
      [stockBindingId("search-open")]: "Mod+' D",
    });
  });

  it("refuses a leader that collides with a catalog chord", async () => {
    const result = await saveShortcutOverride("nav.leader", "Mod+K");
    expect(result).toEqual({ ok: false, reason: expect.any(String) });
    expect(shortcutOverrides()["nav.leader"]).toBeUndefined();
  });

  it("refuses a leader that would shadow a custom declaration", async () => {
    await saveShortcutOverride(stockBindingId("search-open"), "Mod+'");
    const result = await saveShortcutOverride("nav.leader", "Mod+'");
    expect(result).toEqual({
      ok: false,
      reason: expect.stringContaining("Open Crossbar"),
    });
    expect(shortcutOverrides()).toEqual({
      [stockBindingId("search-open")]: "Mod+'",
    });
  });

  it("refuses a Shift-only leader", async () => {
    const result = await saveShortcutOverride("nav.leader", "Shift+;");
    expect(result.ok).toBe(false);
    expect(shortcutOverrides()["nav.leader"]).toBeUndefined();
  });

  describe("refusals carry a reason", () => {
    it("names the system reservation", async () => {
      const result = await saveShortcutOverride(stockBindingId("search-open"), "Mod+Q");
      expect(result).toEqual({
        ok: false,
        reason: expect.stringContaining("reserved by the system"),
      });
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBeUndefined();
    });

    it("names the screen-reader reservation", async () => {
      const result = await saveShortcutOverride(stockBindingId("search-open"), "Mod+Insert");
      expect(result).toEqual({
        ok: false,
        reason: expect.stringContaining("screen readers"),
      });
    });

    it("refuses a chord that is the resolved leader", async () => {
      // A leader chord is unreachable as an ordinary binding.
      const result = await saveShortcutOverride(stockBindingId("search-open"), "Mod+;");
      expect(result).toEqual({
        ok: false,
        reason: expect.stringContaining("leader key"),
      });
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBeUndefined();
    });

    it("refuses a sequence under a foreign leader", async () => {
      const result = await saveShortcutOverride(stockBindingId("search-open"), "Mod+' D");
      expect(result).toEqual({
        ok: false,
        reason: expect.stringContaining("leader key"),
      });
    });

    it("names the command already holding a chord in the same scope", async () => {
      // Both declarations are global.
      const result = await saveShortcutOverride(stockBindingId("session-new"), "Mod+Shift+N");
      expect(result).toEqual({
        ok: false,
        reason: expect.stringContaining("New project"),
      });
      expect(shortcutOverrides()[stockBindingId("session-new")]).toBeUndefined();
    });

    it("allows a chord another stratum already uses", async () => {
      // Join lines controls Mod+J in files scope.
      const result = await saveShortcutOverride(stockBindingId("search-open"), "Mod+J");
      expect(result).toEqual({ ok: true });
      expect(shortcutOverrides()[stockBindingId("search-open")]).toBe("Mod+J");
    });

    it("allows a sequence another stratum already uses", async () => {
      await saveShortcutOverride(stockBindingId("search-open"), "Mod+; Q");
      const result = await saveShortcutOverride(
        stockBindingId("composer-send"),
        "Mod+; Q",
      );
      expect(result).toEqual({ ok: true });
      expect(shortcutOverrides()[stockBindingId("composer-send")]).toBe(
        "Mod+; Q",
      );
    });

    it("names the command already holding a sequence", async () => {
      await saveShortcutOverride(stockBindingId("go-chat"), "Mod+; D");
      const result = await saveShortcutOverride(stockBindingId("go-context-files"), "Mod+; D");
      expect(result).toEqual({
        ok: false,
        reason: expect.stringContaining("Focus chat"),
      });
      expect(shortcutOverrides()["go.files"]).toBeUndefined();
    });

    it("refuses a chord this platform cannot produce", async () => {
      setShortcutPlatformForTests("linux");
      try {
        // Linux emits Ctrl as Mod.
        const result = await saveShortcutOverride(stockBindingId("search-open"), "Ctrl+Tab");
        expect(result).toEqual({
          ok: false,
          reason: expect.stringContaining("can’t be produced"),
        });
      } finally {
        setShortcutPlatformForTests(null);
      }
    });
  });

  it("resetting the leader rehomes its child sequences to the default", async () => {
    await saveShortcutOverride("nav.leader", "Mod+'");
    await saveShortcutOverride(stockBindingId("go-chat"), "Mod+' D");
    await clearShortcutOverride("nav.leader");

    // Stored child sequences follow the active leader.
    expect(shortcutOverrides()).toEqual({ [stockBindingId("go-chat")]: "Mod+; D" });
    expect(
      resolveKeymap(shortcutOverrides(), liveKeymapSource("macos")).byCommand.get(stockId("go-chat")),
    ).toEqual(["Mod+; D"]);
  });
});
