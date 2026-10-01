// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  contributionFrameState,
  resetContributionStoreForTest,
  seedContributionFrameForTest,
  type ContributionFrameState,
} from "../../contributions/contribution-store.ts";
import { STOCK_FRAME } from "../../contributions/stock-frame.generated.ts";
import { getAppStateSnapshot, resetAppStateSnapshotForTests, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import type { ContributionFrameResponse, ContributionTheme } from "../../api/types.ts";
import {
  reconcileOsColorScheme,
  resetOsColorSchemeWatchForTests,
} from "../../theme.ts";
import { clearTheme } from "../../contributions/theme-application.ts";

const syncWindowBackdrop = vi.fn();

vi.mock("../../platform/windows/window-backdrop.ts", () => ({
  syncWindowBackdrop: (...args: unknown[]) => syncWindowBackdrop(...args),
}));

import {
  activeThemePaintKey,
  applyActiveTheme,
  applyContributionThemeFrame,
  resetAppearancePrefsForTests,
  setAppearanceMode,
  setAppearanceTheme,
  syncAppearanceFromSnapshot,
} from "./appearance-prefs.ts";

function customDarkTheme(): ContributionTheme {
  const stock = STOCK_FRAME.themes.find((theme) => theme.appearance === "dark")!;
  return {
    ...stock,
    id: "acme/kit:nightshade",
    name: "Nightshade",
    tokens: { ...stock.tokens, background: "#0b0b10", accent: "#4466aa" },
    logomark: "hidden",
  };
}

function frameWith(theme: ContributionTheme): ContributionFrameResponse {
  return {
    ...STOCK_FRAME,
    frame_revision: "custom-theme-frame",
    themes: [...STOCK_FRAME.themes, theme],
  };
}

function emptyFrameState(
  phase: "unavailable",
): ContributionFrameState {
  return { phase, frame: null };
}

beforeEach(() => {
  syncWindowBackdrop.mockClear();
  resetAppStateSnapshotForTests();
  resetAppearancePrefsForTests();
  resetOsColorSchemeWatchForTests();
  clearTheme();
  document.documentElement.style.colorScheme = "";
  localStorage.clear();
  seedContributionFrameForTest(STOCK_FRAME);
});

afterEach(() => {
  resetContributionStoreForTest();
  resetAppearancePrefsForTests();
  localStorage.clear();
});

describe("applyActiveTheme", () => {
  it("pushes the stock theme background onto the native backdrop", () => {
    applyActiveTheme();
    expect(syncWindowBackdrop).toHaveBeenCalledWith(
      "system",
      expect.stringMatching(/^(light|dark)$/),
      {
        light: expect.stringMatching(/^#[0-9a-f]{6}$/i),
        dark: expect.stringMatching(/^#[0-9a-f]{6}$/i),
      },
    );
  });

  it("preserves the cold-start paint while the first frame hydrates", () => {
    resetContributionStoreForTest();
    applyActiveTheme();
    expect(syncWindowBackdrop).not.toHaveBeenCalled();
  });
});

describe("syncAppearanceFromSnapshot", () => {
  it("applies the stored mode before pushing the backdrop", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "light" },
    });
    syncAppearanceFromSnapshot();
    expect(syncWindowBackdrop).toHaveBeenCalledWith(
      "light",
      "light",
      {
        light: expect.stringMatching(/^#[0-9a-f]{6}$/i),
        dark: expect.stringMatching(/^#[0-9a-f]{6}$/i),
      },
    );
  });

  it("keeps the forced scheme authoritative when the OS changes", () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "light" },
    });
    syncAppearanceFromSnapshot();

    reconcileOsColorScheme(true);

    expect(document.documentElement.dataset.denAppearance).toBe("light");
    expect(document.documentElement.style.colorScheme).toBe("light");
  });

  it("persists theme choices without deleting font choices", async () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { uiFont: "Literata", monoFont: "Berkeley Mono" },
    });
    syncAppearanceFromSnapshot();

    await setAppearanceMode("dark");

    expect(getAppStateSnapshot()).toEqual(expect.objectContaining({
      appearance: expect.objectContaining({
        mode: "dark",
        uiFont: "Literata",
        monoFont: "Berkeley Mono",
      }),
    }));
  });

  it("updates both cold-start schemes when the inactive theme changes", async () => {
    const nightshade = customDarkTheme();
    seedContributionFrameForTest(frameWith(nightshade));
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "light" },
    });
    syncAppearanceFromSnapshot();
    syncWindowBackdrop.mockClear();

    await setAppearanceTheme(nightshade.id, "dark");

    const memo = JSON.parse(
      localStorage.getItem("paintedwolf.boot-theme")!,
    ) as { dark?: { style: string } };
    expect(memo.dark?.style).toContain("--den-background:#0b0b10");
    expect(syncWindowBackdrop).toHaveBeenLastCalledWith("light", "light", {
      light: expect.stringMatching(/^#[0-9a-f]{6}$/i),
      dark: "#0b0b10",
    });
  });

  it("keeps the complete theme painted while the next frame hydrates", () => {
    const nightshade = customDarkTheme();
    seedContributionFrameForTest(frameWith(nightshade));
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "dark", darkTheme: nightshade.id },
    });
    syncAppearanceFromSnapshot();
    expect(document.documentElement.dataset.denLogomark).toBe("hidden");

    applyContributionThemeFrame(contributionFrameState());

    expect(document.documentElement.dataset.denLogomark).toBe("hidden");
    expect(
      document.documentElement.style.getPropertyValue("--den-background"),
    ).toBe("#0b0b10");
    expect(activeThemePaintKey()).not.toBe("stock:dark");
    expect(syncWindowBackdrop).not.toHaveBeenLastCalledWith("dark", "dark", {});
  });

  it("restores stock only after the incoming frame is unavailable", () => {
    const nightshade = customDarkTheme();
    seedContributionFrameForTest(frameWith(nightshade));
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "dark", darkTheme: nightshade.id },
    });
    syncAppearanceFromSnapshot();

    applyContributionThemeFrame(emptyFrameState("unavailable"));

    expect(document.documentElement.dataset.denLogomark).toBeUndefined();
    expect(
      document.documentElement.style.getPropertyValue("--den-background"),
    ).toBe("");
    expect(activeThemePaintKey()).toBe("stock:dark");
    expect(syncWindowBackdrop).toHaveBeenLastCalledWith("dark", "dark", {});
  });

  it("re-resolves preference changes from the last complete frame while loading", async () => {
    const nightshade = customDarkTheme();
    const frame = frameWith(nightshade);
    seedContributionFrameForTest(frame);
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      appearance: { mode: "dark", darkTheme: nightshade.id },
    });
    syncAppearanceFromSnapshot();
    applyContributionThemeFrame(contributionFrameState());

    await setAppearanceMode("light");

    const stockLight = STOCK_FRAME.themes.find(
      (theme) => theme.appearance === "light",
    )!;
    expect(document.documentElement.dataset.denAppearance).toBe("light");
    expect(
      document.documentElement.style.getPropertyValue("--den-background"),
    ).toBe(stockLight.tokens.background);
  });
});
