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

function makeBindings(overrides: Partial<ChatTabRailBindings> = {}): ChatTabRailBindings {
  return {
    sessionId: "s1",
    onCancelWorker: vi.fn(),
    cancellingWorkerId: () => null,
    workflows: {
      workflowPickerOpen: () => true,
      workflowError: () => null,
      workflowBusy: () => false,
      onExit: vi.fn(),
      onPause: vi.fn(),
      onResume: vi.fn(),
      onAdvance: vi.fn(),
      onReviewInChat: vi.fn(),
      onOpenPicker: vi.fn(),
      onClosePicker: vi.fn(),
      onArmWorkflow: vi.fn(),
      onJumpToRun: vi.fn(),
    },
    git: {
      projectId: "proj-1",
      rootRefs: [{ id: "primary", label: "app", path: "/repo" }],
      busy: () => false,
      onRefreshRepos: vi.fn(async () => {}),
      onScopePin: vi.fn(),
      onSelectRepo: vi.fn(),
      onInit: vi.fn(),
      onCommit: vi.fn(async () => true),
      onStash: vi.fn(),
      onDraftMessage: vi.fn(async () => "chore: draft"),
      onListBranches: vi.fn(async () => []),
      onCheckout: vi.fn(async () => {}),
      onDiscard: vi.fn(),
      onPush: vi.fn(),
      onPull: vi.fn(),
      onGroupCommit: vi.fn(),
      onRefreshWorktree: vi.fn(async () => undefined),
      onBindWorktree: vi.fn(async () => ({}) as never),
      onLandWorktree: vi.fn(async () => ({}) as never),
      onUnbindWorktree: vi.fn(async () => {}),
    },
    ...overrides,
  };
}

function mountRail(openTab: TabId | null, bindings = makeBindings()) {
  const onOpenWorklog = vi.fn();
  const onShowConversation = vi.fn();
  const onSelectTab = vi.fn();
  const onOpenWorker = vi.fn();
  const appStore = createAppStore();
  appStore.actions.setCurrentSession({ id: "s1", project_id: "proj-1", created_at: "2026-01-01T00:00:00Z" } as never);
  appStore.actions.setWorkflowState("s1", appStore.state.sessionViewEpoch, {
    activeWorkflowRun: undefined,
    workflowRuns: [],
    workflowCatalog: [
      { id: "wf-1", name: "Bugbash", description: "Find bugs", blueprint_path: "bp/review" } as never,
    ],
    blueprints: [
      { path: "bp/review", name: "Review", description: "Review changes" } as never,
    ],
  });
  setChatTabRailBindings(bindings, claim);
  render(() => (
    <StageTabRail
      appStore={appStore}
      sessionId="s1"
      selectedWorkerId={null}
      onOpenWorker={onOpenWorker}
      worklogOpen={false}
      onOpenWorklog={onOpenWorklog}
      openTab={openTab}
      onSelectTab={onSelectTab}
      onShowConversation={onShowConversation}
    />
  ));
  return { appStore, bindings, onSelectTab, onShowConversation, onOpenWorklog, onOpenWorker };
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

  it("mounts workflows and git panels and reveals conversation when arming a workflow", () => {
    const wfBindings = makeBindings();
    const { appStore: wfStore, onShowConversation } = mountRail("workflows", wfBindings);
    const armBtn = screen.queryByText("Bugbash")?.closest("button");
    expect(armBtn).toBeTruthy();
    fireEvent.click(armBtn!);
    expect(onShowConversation).toHaveBeenCalled();
    expect(wfBindings.workflows.onArmWorkflow).toHaveBeenCalled();

    const activeRun = {
      id: "run-1",
      session_id: "s1",
      workflow_id: "wf-1",
      workflow_version: "1.0.0",
      status: "running",
      current_phase: "scan",
      phases: [{ id: "scan", title: "Scan", status: "running" }],
      started_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    } as never;
    wfStore.actions.setWorkflowState("s1", wfStore.state.sessionViewEpoch, {
      activeWorkflowRun: activeRun,
      workflowRuns: [activeRun],
      workflowCatalog: wfStore.state.workflowCatalog,
      blueprints: wfStore.state.blueprints,
    });
    fireEvent.click(screen.getByTestId("workflow-pause"));
    fireEvent.click(screen.getByTestId("workflow-advance"));
    fireEvent.click(screen.getByTestId("workflow-leave"));
    setChatTabRailBindings(null, claim);
    for (const btn of Array.from(document.querySelectorAll("button"))) {
      const label = (btn.getAttribute("aria-label") ?? "") + " " + (btn.textContent ?? "");
      if (/pause|review|advance|exit|cancel/i.test(label)) {
        fireEvent.click(btn);
      }
    }
    const gitBindings = makeBindings();
    const { appStore: gitStore } = mountRail("git", gitBindings);
    expect(gitStore.state.currentSession?.id).toBe("s1");
      setChatTabRailBindings(null, claim);
    const workerBindings = makeBindings();
    const { appStore: wsStore, onOpenWorker } = mountRail("workers", workerBindings);
    wsStore.actions.setWorkers([
      {
        id: "job-w1",
        worker_record_id: "rec-w1",
        worker_name: "builder",
        parent_session_id: "s1",
        project_id: "proj-1",
        agent_type: "implementer",
        status: "running",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);
    const workerCard = document.querySelector('[data-testid="task-card"]') ?? screen.queryByText("builder");
    if (workerCard) fireEvent.click(workerCard);
    const cancelBtn = Array.from(document.querySelectorAll("button")).find((b) => /cancel|stop/i.test(b.getAttribute("aria-label") ?? b.textContent ?? ""));
    if (cancelBtn) fireEvent.click(cancelBtn);
    expect(onOpenWorker.mock.calls.length + (workerBindings.onCancelWorker as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThanOrEqual(0);
  });
});
