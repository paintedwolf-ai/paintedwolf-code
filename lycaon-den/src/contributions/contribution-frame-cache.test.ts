// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { STOCK_FRAME } from "./stock-frame.generated.ts";
import { CONTRIBUTION_FRAME_STORAGE_KEY, clearContributionFrameCache, readContributionFrameCache, writeContributionFrameCache } from "./contribution-frame-cache.ts";
import { contributionFrame, replayContributionFrameMemoForTest, resetContributionStoreForTest } from "./contribution-store.ts";
import { syncAppearanceFromSnapshot, resetAppearancePrefsForTests } from "../settings/appearance/appearance-prefs.ts";
import { clearTheme } from "./theme-application.ts";

afterEach(() => { resetContributionStoreForTest(); resetAppearancePrefsForTests(); clearTheme(); clearContributionFrameCache(); });

describe("contribution frame cache", () => {
  it("replays a complete current frame", () => {
    writeContributionFrameCache(STOCK_FRAME);
    expect(readContributionFrameCache()).toEqual(STOCK_FRAME);
  });

  it("discards incompatible cache formats without touching device preferences", () => {
    localStorage.setItem("device-preferences-test", "retained");
    for (const memo of [STOCK_FRAME, { version: 999, frame: STOCK_FRAME }, { version: 1, frame: null }]) {
      localStorage.setItem(CONTRIBUTION_FRAME_STORAGE_KEY, JSON.stringify(memo));
      expect(readContributionFrameCache()).toBeNull();
      expect(localStorage.getItem(CONTRIBUTION_FRAME_STORAGE_KEY)).toBeNull();
      expect(localStorage.getItem("device-preferences-test")).toBe("retained");
    }
    localStorage.removeItem("device-preferences-test");
  });

  it("keeps appearance bootstrap reachable when a cached theme lacks its required recipe", () => {
    writeContributionFrameCache(STOCK_FRAME);
    const memo = JSON.parse(localStorage.getItem(CONTRIBUTION_FRAME_STORAGE_KEY) ?? "null") as { frame: { themes: Record<string, unknown>[] } };
    for (const theme of memo.frame.themes) delete theme.window_colors;
    localStorage.setItem(CONTRIBUTION_FRAME_STORAGE_KEY, JSON.stringify(memo));
    replayContributionFrameMemoForTest();
    expect(contributionFrame()).toBeNull();
    expect(() => syncAppearanceFromSnapshot()).not.toThrow();
    expect(localStorage.getItem(CONTRIBUTION_FRAME_STORAGE_KEY)).toBeNull();
  });
});
