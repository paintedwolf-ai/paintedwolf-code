// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createRoot, createComputed } from "solid-js";
import { EMPTY_APP_STATE_V1 } from "../../shared/app-state-types.ts";
import {
  beginListColumnResize,
  beginListPaneResize,
  beginNavWidthResize,
  beginChatWidthResize,
  companionStageId,
  companionPref,
  rememberCompanionStage,
  currentSplitFocusRegion,
  effectiveNavWidthPx,
  effectiveChatWidthPx,
  listPanePrefs,
  preferredNavWidthPx,
  preferredChatWidthPx,
  resetChatWidthPx,
  resetLayoutStoreForTests,
  saveStagePlacementMode,
  saveStartupCompanion,
  setLayoutViewportWidth,
  setSplitFocusRegion,
  setSplitHostWidth,
  setSplitProjectId,
  stagePlacementMode,
  startupCompanionPref,
  syncLayoutFromSnapshot,
  workspaceOrientationPref,
  splitOrderPref,
  swapSplitColumns,
  toggleWorkspaceOrientation,
} from "./layout-store.ts";
import {
  flushShellLayoutSettleForTests,
  isShellLayoutUnstable,
} from "./shell-layout-busy.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../store/app-state-snapshot.ts";
import {
  CHAT_WIDTH_DEFAULT_PX,
  DIVIDER_PX,
  STAGE_COL_MIN,
} from "./stage-placement.ts";

describe("layout store", () => {
  beforeEach(() => {
    localStorage.clear();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetLayoutStoreForTests();
  });

  afterEach(() => {
    resetLayoutStoreForTests();
  });

  it("persists independent split order without disturbing pane state", async () => {
    const layout = { mode: "split", companion: "files", navCollapsed: true,
      hiddenSplitPane: "conversation", chatWidthPx: 520, workspaceOrientation: "standard" } as const;
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1, layout });
    syncLayoutFromSnapshot();
    setSplitFocusRegion("stage");
    expect(splitOrderPref()).toBe("context-first");
    await swapSplitColumns();
    expect(getAppStateSnapshot().layout).toEqual({ ...layout, splitOrder: "chat-first" });
    expect(currentSplitFocusRegion()).toBe("stage");
    await toggleWorkspaceOrientation();
    expect(splitOrderPref()).toBe("chat-first");
    expect(workspaceOrientationPref()).toBe("mirrored");
    await swapSplitColumns();
    expect(getAppStateSnapshot().layout).toEqual({ ...layout, workspaceOrientation: "mirrored", splitOrder: "context-first" });
    syncLayoutFromSnapshot();
    expect(splitOrderPref()).toBe("context-first");
  });

  it("cancels an unfinished divider resize before changing its direction", async () => {
    const tx = beginChatWidthResize(1500);
    tx.preview(600);
    await swapSplitColumns();
    tx.commit();
    expect(preferredChatWidthPx()).toBe(CHAT_WIDTH_DEFAULT_PX);
    expect(effectiveChatWidthPx(1500)).toBe(CHAT_WIDTH_DEFAULT_PX);
  });

  it("restores preferred nav width after a responsive clamp", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { navWidthPx: 420 },
    });
    syncLayoutFromSnapshot();
    setLayoutViewportWidth(600);
    expect(effectiveNavWidthPx()).toBe(270);
    expect(preferredNavWidthPx()).toBe(420);

    setLayoutViewportWidth(1200);
    expect(effectiveNavWidthPx()).toBe(420);
  });

  it("does not persist or rewrite a preference for a no-op gesture", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { navWidthPx: 360 },
    });
    syncLayoutFromSnapshot();
    setLayoutViewportWidth(1200);

    const session = beginNavWidthResize();
    session.preview(400);
    session.preview(360);
    session.commit();

    expect(getAppStateSnapshot().layout).toEqual({ navWidthPx: 360 });
  });

  it("publishes previews atomically and cancellation restores persisted state", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { navWidthPx: 320 },
    });
    syncLayoutFromSnapshot();
    setLayoutViewportWidth(1200);

    const session = beginNavWidthResize();
    session.preview(410);
    expect(effectiveNavWidthPx()).toBe(410);
    session.cancel();

    expect(effectiveNavWidthPx()).toBe(320);
    expect(getAppStateSnapshot().layout).toEqual({ navWidthPx: 320 });
  });

  it("never publishes a persisted-width rebound when committing a live resize", () => {
    setLayoutViewportWidth(1200);
    const observed: number[] = [];
    createRoot((dispose) => {
      createComputed(() => observed.push(effectiveNavWidthPx()));
      const session = beginNavWidthResize();
      session.preview(410);
      observed.length = 0;
      session.commit();
      expect(effectiveNavWidthPx()).toBe(410);
      expect(observed.every((width) => width === 410)).toBe(true);
      dispose();
    });
  });

  it("persists the launch companion and clears it back to chat only", async () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { mode: "split" },
    });
    syncLayoutFromSnapshot();
    expect(startupCompanionPref()).toBeNull();

    await saveStartupCompanion("files");
    expect(startupCompanionPref()).toBe("files");
    expect(getAppStateSnapshot().layout?.startupCompanion).toBe("files");

    await saveStartupCompanion(null);
    expect(startupCompanionPref()).toBeNull();
    expect(getAppStateSnapshot().layout?.startupCompanion).toBeUndefined();
  });

  it("commits the dragged conversation width as the one workspace split", async () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { mode: "split", chatWidthPx: 440 },
    });
    syncLayoutFromSnapshot();

    const session = beginChatWidthResize(1400);
    session.preview(520);
    session.commit();

    expect(preferredChatWidthPx()).toBe(520);
    expect(getAppStateSnapshot().layout?.chatWidthPx).toBe(520);

    await resetChatWidthPx();
    expect(preferredChatWidthPx()).toBe(CHAT_WIDTH_DEFAULT_PX);
    expect(getAppStateSnapshot().layout).not.toHaveProperty("chatWidthPx");
  });

  it("keeps the conversation width while the host grows and shrinks", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { mode: "split", chatWidthPx: 480 },
    });
    syncLayoutFromSnapshot();
    expect(effectiveChatWidthPx(1400)).toBe(480);
    expect(effectiveChatWidthPx(1800)).toBe(480);
    expect(effectiveChatWidthPx(1300)).toBe(480);
  });

  it("stops a drag where the stage would fold its Files tree", () => {
    const session = beginChatWidthResize(1400);
    session.preview(900);
    expect(effectiveChatWidthPx(1400)).toBe(1400 - DIVIDER_PX - STAGE_COL_MIN);
    session.cancel();
  });

  it("keeps the stored width when the companion changes", async () => {
    const session = beginChatWidthResize(1400);
    session.preview(480);
    session.commit();
    await rememberCompanionStage("search");
    expect(preferredChatWidthPx()).toBe(480);
    await rememberCompanionStage("files");
    expect(preferredChatWidthPx()).toBe(480);
  });

  it("keeps a constrained split preference intact until the user moves it", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: {
        mode: "split",
        chatWidthPx: 600,
      },
    });
    syncLayoutFromSnapshot();
    expect(effectiveChatWidthPx(1100)).toBe(
      1100 - DIVIDER_PX - STAGE_COL_MIN,
    );

    beginChatWidthResize(1100).commit();
    expect(preferredChatWidthPx()).toBe(600);
    expect(getAppStateSnapshot().layout?.chatWidthPx).toBe(600);
  });

  it("uses the same transaction semantics for pane and column layout", () => {
    const pane = beginListPaneResize("search", "width", 300, 1000);
    pane.preview(420);
    expect(listPanePrefs("search").widthPx).toBe(420);
    pane.cancel();
    expect(listPanePrefs("search").widthPx).toBeUndefined();

    const column = beginListColumnResize("search", "name", 120, {
      basis: 120,
      min: 80,
      max: 160,
    });
    column.preview(168);
    expect(listPanePrefs("search").columnWidths?.name).toBe(160);
    column.commit();
    expect(getAppStateSnapshot().layout?.listPanes?.search?.columnWidths).toEqual({
      name: 160,
    });
  });

  it("keeps the stored split intent independent of the measured host", async () => {
    await saveStagePlacementMode({ mode: "split", companion: "files" });
    setSplitProjectId("project-a");

    // Recorded for drag clamping and widen; render does not read it.
    setSplitHostWidth(700);
    expect(stagePlacementMode()).toBe("split");
    expect(companionStageId()).toBe("files");

    setSplitHostWidth(1200);
    expect(stagePlacementMode()).toBe("split");
    expect(companionStageId()).toBe("files");
  });

  it("publishes placement and companion inside one settled layout transaction", async () => {
    const saved = saveStagePlacementMode({
      mode: "split",
      companion: "files",
    });
    expect(stagePlacementMode()).toBe("split");
    expect(companionStageId()).toBe("files");
    expect(companionPref()).toBe("files");
    expect(isShellLayoutUnstable()).toBe(true);

    await saved;
    await flushShellLayoutSettleForTests();
    expect(isShellLayoutUnstable()).toBe(false);
    expect(getAppStateSnapshot().layout?.companion).toBe("files");
  });

  it("defaults a hydrated split with no companion to Files", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { mode: "split" },
    });
    syncLayoutFromSnapshot();
    expect(stagePlacementMode()).toBe("split");
    expect(companionPref()).toBeNull();
    expect(companionStageId()).toBe("files");
  });

  it("remembers a companion while the layout is still inline", async () => {
    await rememberCompanionStage("search");
    expect(companionPref()).toBe("search");
    expect(companionStageId()).toBeNull();
    await saveStagePlacementMode({ mode: "split", companion: "search" });
    expect(companionStageId()).toBe("search");
  });

  it("hydrates one unified document", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: {
        navWidthPx: 390,
        mode: "split",
        workspaceOrientation: "mirrored",
        chatWidthPx: 520,
      },
    });
    syncLayoutFromSnapshot();
    expect(workspaceOrientationPref()).toBe("mirrored");
    expect(stagePlacementMode()).toBe("split");
    expect(preferredChatWidthPx()).toBe(520);
  });
});

describe("split column state", () => {
  beforeEach(() => {
    localStorage.clear();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetLayoutStoreForTests();
  });

  afterEach(() => {
    resetLayoutStoreForTests();
  });

  it("starts on the conversation", () => {
    // Fresh splits have not entered the stage column.
    expect(currentSplitFocusRegion()).toBe("chat");
  });

  it("goes back to the conversation when the project changes", () => {
    setSplitProjectId("p1");
    setSplitFocusRegion("stage");
    expect(currentSplitFocusRegion()).toBe("stage");

    setSplitProjectId("p1");
    expect(currentSplitFocusRegion()).toBe("stage");

    // Column focus is per project.
    setSplitProjectId("p2");
    expect(currentSplitFocusRegion()).toBe("chat");
  });

  it("goes back to the conversation when the layout leaves split", async () => {
    await saveStagePlacementMode({ mode: "split", companion: "files" });
    setSplitFocusRegion("stage");

    await saveStagePlacementMode({ mode: "inline" });
    // Inline layout has no stage column.
    expect(currentSplitFocusRegion()).toBe("chat");
  });
});
