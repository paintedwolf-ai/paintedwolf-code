import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { ShellSessionDrawerHost, ShellStageColumn } from "./ShellColumns.tsx";
import { ShellHeaderTitlebar } from "./ShellHeaderTitlebar.tsx";

vi.mock("./ChromeDragSurface.tsx", () => ({ ChromeDragSurface: () => null }));

const conversationAtRight = {
  nav: { collapsed: false, automatic: false, side: "left" as const, onExpand: () => {} },
  conversation: {
    collapsed: true,
    side: "right" as const,
    onExpand: () => {},
    hiddenWhenNarrow: false,
    onWiden: () => {},
    widenRefusals: () => 0,
  },
};

describe("ShellHeaderTitlebar session tabs", () => {
  it("places the session's tabs after the conversation's reopen control", () => {
    render(() => (
      <ShellHeaderTitlebar
        edges={conversationAtRight}
        sessionTabs={{ present: () => true, children: () => <div data-testid="rail" /> }}
      />
    ));
    const rail = screen.getByTestId("rail");
    const reopen = screen.getByTestId("conversation-expand-btn");
    expect(rail.compareDocumentPosition(reopen) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy();
  });

  it("shows the tabs only while the stage carries them", () => {
    const [present, setPresent] = createSignal(false);
    render(() => (
      <ShellHeaderTitlebar
        sessionTabs={{ present, children: () => <div data-testid="rail" /> }}
      />
    ));
    expect(screen.queryByTestId("rail")).toBeNull();
    setPresent(true);
    expect(screen.getByTestId("rail")).toBeTruthy();
    setPresent(false);
    expect(screen.queryByTestId("rail")).toBeNull();
  });

  it("mounts StageTabRail inside ShellStageColumn and renders worklog/worker drawers via ShellSessionDrawerHost", () => {
    const appStore = createAppStore();
    appStore.actions.setWorkers([
      {
        id: "w1",
        child_session_id: "child-1",
        parent_session_id: "s1",
        project_id: "p1",
        agent_type: "implementer",
        status: "running",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);
    const onClose = vi.fn();
    const onOpenWorker = vi.fn();
    const [content, setContent] = createSignal<"worklog" | "worker" | null>("worklog");

    render(() => (
      <div>
        <ShellStageColumn
          occupied
          stageId="files"
          stack={{ keys: () => [], presence: () => "active", pending: () => null, withdraw: () => {}, markReady: () => {} } as never}
          back={null}
          edges={conversationAtRight}
          windowEdges={[]}
          appStore={appStore}
          sessionTabs={{
            sessionId: () => "s1",
            openTab: () => null,
            selectTab: vi.fn(),
            showConversation: vi.fn(),
          }}
          workers={{
            selectedWorkerId: () => "w1",
            openWorkers: onOpenWorker,
            worklogOpen: () => content() === "worklog",
            openWorklog: vi.fn(),
          }}
          notifications={null}
          waitingVisible={false}
          preparation={undefined}
          onReady={vi.fn()}
        >
          {() => <div />}
        </ShellStageColumn>
        <ShellSessionDrawerHost
          appStore={appStore}
          projects={[]}
          projectDir="/repo"
          sessionId="s1"
          content={content()}
          selectedWorkerId="w1"
          workerFocus={null}
          onWorkerFocusHandled={vi.fn()}
          backgroundHydrate={false}
          onOpenWorker={onOpenWorker}
          onClose={onClose}
        />
      </div>
    ));

    expect(screen.getByTestId("stage-tab-progress")).toBeTruthy();
    setContent("worker");
    const closeButtons = document.querySelectorAll('button[aria-label*="Close"]');
    if (closeButtons[0]) {
      fireEvent.click(closeButtons[0]);
      expect(onClose).toHaveBeenCalled();
    }
  });
});
