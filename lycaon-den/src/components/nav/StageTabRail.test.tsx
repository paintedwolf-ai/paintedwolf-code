import { fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  setChatTabRailBindings,
  type ChatTabRailBindings,
} from "../../chat/composer/chat-tab-rail-bindings.ts";
import { createAppStore } from "../../store/app-state.ts";
import type { TabId } from "../chatview/tabs.ts";
import { StageTabRail } from "./StageTabRail.tsx";

const claim = {};

function mountRail(openTab: TabId | null) {
  const order: string[] = [];
  const onOpenWorklog = vi.fn(() => order.push("worklog"));
  const onShowConversation = vi.fn(() => order.push("conversation"));
  const onSelectTab = vi.fn();
  setChatTabRailBindings(
    { sessionId: "s1", onOpenWorklog, worklogOpen: () => false } as unknown as ChatTabRailBindings,
    claim,
  );
  render(() => (
    <StageTabRail
      appStore={createAppStore()}
      sessionId="s1"
      selectedWorkerId={null}
      onOpenWorker={() => {}}
      openTab={openTab}
      onSelectTab={onSelectTab}
      onShowConversation={onShowConversation}
    />
  ));
  return { order, onSelectTab, onShowConversation };
}

describe("StageTabRail", () => {
  afterEach(() => setChatTabRailBindings(null, claim));

  it("offers the session's four tabs as chips that select a tab", () => {
    const { onSelectTab } = mountRail(null);
    for (const id of ["progress", "workflows", "git", "workers"]) {
      expect(screen.getByTestId(`stage-tab-${id}`)).toBeTruthy();
    }
    fireEvent.click(screen.getByTestId("stage-tab-git"));
    expect(onSelectTab).toHaveBeenCalledWith("git");
  });

  it("puts the conversation on screen before opening the worklog in it", () => {
    const { order } = mountRail("progress");
    fireEvent.click(screen.getByTestId("worklog-open"));
    expect(order).toEqual(["conversation", "worklog"]);
  });

  it("leaves the conversation off screen for the stage's own panel", () => {
    const { onShowConversation } = mountRail("workers");
    expect(screen.getByTestId("stage-tabs-panel").textContent).toContain("No workers");
    expect(onShowConversation).not.toHaveBeenCalled();
  });
});
