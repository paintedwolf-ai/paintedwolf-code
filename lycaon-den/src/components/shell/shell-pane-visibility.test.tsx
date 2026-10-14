import { createRoot } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1, type SplitOrder, type WorkspaceOrientation } from "../../../shared/app-state-types.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { resetLayoutForTests } from "../../layout/layout-test-support.ts";
import { syncLayoutFromSnapshot } from "../../layout/layout-preferences.ts";
import { setLayoutViewportWidth } from "../../shell/workspace-layout.ts";
import { createShellPaneVisibility } from "./shell-pane-visibility.ts";

afterEach(() => resetLayoutForTests());

function panes(orientation: WorkspaceOrientation, splitOrder: SplitOrder, hidden: boolean, opts?: {
  navCollapsed?: boolean; splitLive?: boolean; stageOccupied?: boolean; hiddenPane?: "stage" | "conversation";
}) {
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1,
    layout: { workspaceOrientation: orientation, splitOrder, navCollapsed: opts?.navCollapsed ?? true,
      hiddenSplitPane: hidden ? "conversation" : opts?.hiddenPane } });
  syncLayoutFromSnapshot();
  // Wide enough that nothing collapses on its own.
  setLayoutViewportWidth(2400);
  let dispose!: () => void;
  const value = createRoot((d) => {
    dispose = d;
    return createShellPaneVisibility({ windowSubject: null, splitLive: () => opts?.splitLive ?? true,
      presentedSplitLive: () => opts?.splitLive ?? true, presentedStageId: () => "files",
      stageColumnOccupied: () => opts?.stageOccupied ?? true });
  });
  return { value, dispose };
}

describe("split chrome visibility", () => {
  it.each(["standard", "mirrored"] as const)("routes adjacent restores and window clearance in %s", (orientation) => {
    for (const order of ["context-first", "chat-first"] as const) {
      const { value, dispose } = panes(orientation, order, false);
      const chatFirst = order === "chat-first";
      const stageLeft = (orientation === "standard") !== chatFirst;
      expect(value.stageIsLeading()).toBe(stageLeft);
      expect(value.navTouchesChat()).toBe(chatFirst);
      expect(value.stageEdges(undefined).nav.onlyWhenNarrow).toBe(chatFirst);
      expect(value.stageHeaderLeadsWindow()).toBe(stageLeft);
      expect(value.chatHeaderLeadsWindow()).toBe(!stageLeft);
      expect(value.conversationSeam()?.side).toBe(stageLeft ? "right" : "left");
      dispose();
    }
  });

  it.each(["standard", "mirrored"] as const)("transfers sidebar restore and clearance to context when chat hides in %s", (orientation) => {
    const { value, dispose } = panes(orientation, "chat-first", true);
    expect(value.stageEdges(undefined).nav.onlyWhenNarrow).toBe(false);
    expect(value.stageEdges(undefined).conversation.side).toBe(value.navSide());
    expect(value.stageHeaderLeadsWindow()).toBe(true);
    expect(value.conversationSeam()).toBeUndefined();
    dispose();
  });
});

describe("window edge hosts", () => {
  const hosts = (value: ReturnType<typeof createShellPaneVisibility>) =>
    [value.windowEdgeHosts("left"), value.windowEdgeHosts("right")];

  it("gives a visible sidebar its own window edge", () => {
    const { value, dispose } = panes("standard", "context-first", false, { navCollapsed: false });
    expect(hosts(value)).toEqual([{ owner: "nav", narrow: "nav" }, { owner: "chat", narrow: "chat" }]);
    dispose();
    const mirrored = panes("mirrored", "context-first", false, { navCollapsed: false });
    expect(hosts(mirrored.value)).toEqual([{ owner: "chat", narrow: "chat" }, { owner: "nav", narrow: "nav" }]);
    mirrored.dispose();
  });

  it("splits the edges between the stage and the chat once the sidebar collapses", () => {
    const { value, dispose } = panes("standard", "context-first", false);
    expect(hosts(value)).toEqual([{ owner: "stage", narrow: "chat" }, { owner: "chat", narrow: "chat" }]);
    dispose();
    const chatFirst = panes("standard", "chat-first", false);
    expect(hosts(chatFirst.value)).toEqual([{ owner: "chat", narrow: "chat" }, { owner: "stage", narrow: "chat" }]);
    chatFirst.dispose();
  });

  it("hands the dropped column's edge to the narrow survivor", () => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1,
      layout: { workspaceOrientation: "standard", splitOrder: "context-first", navCollapsed: true, narrowSurvivor: "stage" } });
    syncLayoutFromSnapshot();
    setLayoutViewportWidth(2400);
    const value = createRoot(() => createShellPaneVisibility({ windowSubject: null, splitLive: () => true,
      presentedSplitLive: () => true, presentedStageId: () => "files", stageColumnOccupied: () => true }));
    expect(hosts(value)).toEqual([{ owner: "stage", narrow: "stage" }, { owner: "chat", narrow: "stage" }]);
    expect(value.windowEdgeClaims("chat")).toEqual([
      { side: "left", owner: false, narrow: false }, { side: "right", owner: true, narrow: false },
    ]);
    expect(value.windowEdgeClaims("stage")).toEqual([
      { side: "left", owner: true, narrow: true }, { side: "right", owner: false, narrow: true },
    ]);
  });

  it("gives the surviving column both edges when a split pane is hidden", () => {
    const chatHidden = panes("standard", "context-first", true);
    expect(hosts(chatHidden.value)).toEqual([{ owner: "stage", narrow: "stage" }, { owner: "stage", narrow: "stage" }]);
    chatHidden.dispose();
    const stageHidden = panes("standard", "context-first", false, { hiddenPane: "stage" });
    expect(hosts(stageHidden.value)).toEqual([{ owner: "chat", narrow: "chat" }, { owner: "chat", narrow: "chat" }]);
    stageHidden.dispose();
  });

  it("gives a lone column both edges outside a split", () => {
    const stage = panes("standard", "context-first", false, { splitLive: false });
    expect(hosts(stage.value)).toEqual([{ owner: "stage", narrow: "stage" }, { owner: "stage", narrow: "stage" }]);
    stage.dispose();
    const chat = panes("standard", "context-first", false, { splitLive: false, stageOccupied: false });
    expect(hosts(chat.value)).toEqual([{ owner: "chat", narrow: "chat" }, { owner: "chat", narrow: "chat" }]);
    chat.dispose();
  });
});
