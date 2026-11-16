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
  const onOpenWorklog = vi.fn();
  const onShowConversation = vi.fn();
  const onSelectTab = vi.fn();
  setChatTabRailBindings(
    { sessionId: "s1" } as unknown as ChatTabRailBindings,
    claim,
  );
  render(() => (
    <StageTabRail
      appStore={createAppStore()}
      sessionId="s1"
      selectedWorkerId={null}
      onOpenWorker={() => {}}
      worklogOpen={false}
      onOpenWorklog={onOpenWorklog}
      openTab={openTab}
      onSelectTab={onSelectTab}
      onShowConversation={onShowConversation}
    />
  ));
  return { onSelectTab, onShowConversation, onOpenWorklog };
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

  it("opens the worklog drawer and leaves the conversation off screen", () => {
    const { onOpenWorklog, onShowConversation } = mountRail("progress");
    fireEvent.click(screen.getByTestId("worklog-open"));
    expect(onOpenWorklog).toHaveBeenCalledOnce();
    expect(onShowConversation).not.toHaveBeenCalled();
  });

  it("leaves the conversation off screen for the stage's own panel", () => {
    const { onShowConversation } = mountRail("workers");
    expect(screen.getByTestId("stage-tabs-panel").textContent).toContain("No workers");
    expect(onShowConversation).not.toHaveBeenCalled();
  });
});
