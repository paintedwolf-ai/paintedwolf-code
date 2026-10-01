// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const mainWindowPath = window.location.pathname;

afterEach(() => {
  window.history.replaceState({}, "", mainWindowPath);
  vi.resetModules();
});

describe("layout store in a peer window", () => {
  it("seeds from main preferences once, then keeps peer layout ephemeral", async () => {
    window.history.replaceState(
      {},
      "",
      "?window_subject=session&project_id=project-1&session_id=session-1&view_id=1",
    );
    const { EMPTY_APP_STATE_V1 } = await import("../../shared/app-state-types.ts");
    const { getAppStateSnapshot, setAppStateSnapshot } = await import(
      "../store/app-state-snapshot.ts"
    );
    const {
      navCollapsedPref,
      saveStagePlacementMode,
      stagePlacementMode,
      syncLayoutFromSnapshot,
      splitOrderPref,
      swapSplitColumns,
    } = await import("./layout-store.ts");

    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      layout: { mode: "split", navCollapsed: false, navWidthPx: 360 },
    });
    syncLayoutFromSnapshot();
    expect(navCollapsedPref()).toBe(true);
    expect(stagePlacementMode()).toBe("inline");

    await swapSplitColumns();
    expect(splitOrderPref()).toBe("chat-first");
    await saveStagePlacementMode({ mode: "split", companion: "files" });
    expect(stagePlacementMode()).toBe("split");
    expect(getAppStateSnapshot().layout).toEqual({
      mode: "split",
      navCollapsed: false,
      navWidthPx: 360,
    });

    syncLayoutFromSnapshot();
    expect(stagePlacementMode()).toBe("split");
    expect(splitOrderPref()).toBe("chat-first");
  });
});
