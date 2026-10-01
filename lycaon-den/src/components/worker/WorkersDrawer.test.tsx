import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@solidjs/testing-library";
import type { JSX } from "solid-js";
import { createAppStore } from "../../store/app-state.ts";
import { WorkersDrawer } from "./WorkersDrawer.tsx";
import { ContextDrawerHost } from "../shell/ContextDrawer.tsx";

const worker = {
  id: "job-1",
  parent_session_id: "sess-1",
  agent_type: "implementer",
  status: "running" as const,
  brief: "Build the adventure game",
  created_at: "2026-01-01T00:00:00Z",
};

function renderInChatStage(children: () => JSX.Element) {
  return render(() => (
    <ContextDrawerHost>
      <div class="den-shell-stage--chat den-shell-stage">
        <header>Chat tabs</header>
        <main>{children()}</main>
      </div>
    </ContextDrawerHost>
  ));
}

describe("WorkersDrawer", () => {
  it("renders the selected worker's transcript with a close control, no picker", () => {
    const appStore = createAppStore();
    renderInChatStage(() => (
      <WorkersDrawer
        open
        appStore={appStore}
        projectDir="/tmp/p"
        workers={[worker]}
        selectedId={worker.id}
        onClose={vi.fn()}
      />
    ));
    expect(screen.getByTestId("workers-drawer")).toBeTruthy();
    expect(screen.getByTestId("worker-transcript")).toBeTruthy();
    // Worker selection belongs to the Workers tab.
    expect(screen.queryByTestId("workers-tab-row")).toBeNull();
  });

  it("closes via the close button", () => {
    const onClose = vi.fn();
    const appStore = createAppStore();
    renderInChatStage(() => (
      <WorkersDrawer
        open
        appStore={appStore}
        projectDir="/tmp/p"
        workers={[worker]}
        selectedId={worker.id}
        onClose={onClose}
      />
    ));
    fireEvent.click(screen.getByLabelText("Close worker"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("renders nothing when closed", () => {
    const appStore = createAppStore();
    renderInChatStage(() => (
      <WorkersDrawer
        open={false}
        appStore={appStore}
        projectDir="/tmp/p"
        workers={[worker]}
        selectedId={worker.id}
        onClose={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("workers-drawer")).toBeNull();
  });
});
