import { createRoot } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1, type SplitOrder, type WorkspaceOrientation } from "../../../shared/app-state-types.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { resetLayoutStoreForTests, syncLayoutFromSnapshot } from "../../shell/layout-store.ts";
import { createShellPaneVisibility } from "./shell-pane-visibility.ts";

afterEach(() => resetLayoutStoreForTests());

function panes(orientation: WorkspaceOrientation, splitOrder: SplitOrder, hidden: boolean) {
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1,
    layout: { workspaceOrientation: orientation, splitOrder, navCollapsed: true, hiddenSplitPane: hidden ? "conversation" : undefined } });
  syncLayoutFromSnapshot();
  let dispose!: () => void;
  const value = createRoot((d) => {
    dispose = d;
    return createShellPaneVisibility({ windowSubject: null, splitLive: () => true,
      presentedSplitLive: () => true, presentedStageId: () => "files" });
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
