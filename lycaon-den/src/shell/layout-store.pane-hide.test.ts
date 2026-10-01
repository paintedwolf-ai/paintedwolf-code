// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  EMPTY_APP_STATE_V1,
  NAV_WIDTH_DEFAULT_PX,
} from "../../shared/app-state-types.ts";
import { PANE_HIDDEN_PX } from "../layout/drag-to-hide.ts";
import { FILES_TREE_SURFACE } from "../list/list-pane-model.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../store/app-state-snapshot.ts";
import {
  beginListPaneResize,
  beginNavWidthResize,
  beginChatWidthResize,
  hiddenSplitPanePref,
  commitHiddenSplitPane,
  conversationHidePreviewed,
  effectiveNavWidthPx,
  effectiveChatWidthPx,
  listPaneHidePreviewed,
  listPanePrefs,
  navHidePreviewed,
  resetLayoutStoreForTests,
  saveStagePlacementMode,
  setLayoutViewportWidth,
} from "./layout-store.ts";
import { CHAT_WIDTH_DEFAULT_PX, withPlacement } from "./stage-placement.ts";

function freshStore() {
  localStorage.clear();
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  resetLayoutStoreForTests();
  setLayoutViewportWidth(1200);
}

describe("hidden split pane", () => {
  beforeEach(freshStore);
  afterEach(resetLayoutStoreForTests);

  it.each(["conversation", "stage"] as const)("persists hidden %s and drops the key once shown", async (pane) => {
    commitHiddenSplitPane(pane);
    expect(hiddenSplitPanePref()).toBe(pane);
    await vi.waitFor(() =>
      expect(getAppStateSnapshot().layout?.hiddenSplitPane).toBe(pane),
    );

    commitHiddenSplitPane(null);
    expect((hiddenSplitPanePref() === "conversation")).toBe(false);
    await vi.waitFor(() =>
      expect(getAppStateSnapshot().layout).not.toHaveProperty("hiddenSplitPane"),
    );
  });

  it("clears when a placement is chosen", async () => {
    commitHiddenSplitPane("conversation");
    await saveStagePlacementMode({ mode: "split", companion: "files" });
    expect((hiddenSplitPanePref() === "conversation")).toBe(false);
    expect(
      withPlacement({ hiddenSplitPane: "conversation", mode: "split" }, { mode: "inline" }),
    ).not.toHaveProperty("hiddenSplitPane");
  });
});

describe("drag to hide", () => {
  beforeEach(freshStore);
  afterEach(resetLayoutStoreForTests);

  it("previews a hidden sidebar and hides it on release without losing its width", () => {
    const onHide = vi.fn();
    const session = beginNavWidthResize(onHide);
    session.preview(PANE_HIDDEN_PX);
    expect(navHidePreviewed()).toBe(true);
    expect(effectiveNavWidthPx()).toBe(NAV_WIDTH_DEFAULT_PX);

    session.commit();
    expect(onHide).toHaveBeenCalledOnce();
    expect(navHidePreviewed()).toBe(false);
    expect(getAppStateSnapshot().layout?.navWidthPx).toBeUndefined();
  });

  it("keeps the sidebar when the drag returns before release", () => {
    const onHide = vi.fn();
    const session = beginNavWidthResize(onHide);
    session.preview(PANE_HIDDEN_PX);
    session.preview(300);
    expect(navHidePreviewed()).toBe(false);

    session.commit();
    expect(onHide).not.toHaveBeenCalled();
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(300);
  });

  it("clamps instead of hiding when the resize offers no hide", () => {
    const session = beginNavWidthResize();
    session.preview(PANE_HIDDEN_PX);
    expect(navHidePreviewed()).toBe(false);
    session.cancel();
  });

  it("hides the conversation from a divider drag and keeps its stored width", () => {
    const onHide = vi.fn();
    const session = beginChatWidthResize(1400, onHide);
    session.preview(PANE_HIDDEN_PX);
    expect(conversationHidePreviewed()).toBe(true);
    expect(effectiveChatWidthPx(1400)).toBe(CHAT_WIDTH_DEFAULT_PX);

    session.commit();
    expect(onHide).toHaveBeenCalledOnce();
    expect(conversationHidePreviewed()).toBe(false);
    expect(getAppStateSnapshot().layout?.chatWidthPx).toBeUndefined();
  });

  it("hides the Files tree from a width drag and keeps its stored width", () => {
    const onHide = vi.fn();
    const session = beginListPaneResize(
      FILES_TREE_SURFACE,
      "width",
      240,
      1000,
      onHide,
    );
    session.preview(PANE_HIDDEN_PX);
    expect(listPaneHidePreviewed(FILES_TREE_SURFACE, "width")).toBe(true);
    expect(listPanePrefs(FILES_TREE_SURFACE).widthPx).toBeUndefined();

    session.commit();
    expect(onHide).toHaveBeenCalledOnce();
    expect(getAppStateSnapshot().layout?.listPanes).toBeUndefined();
  });
});
