import { createRoot, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { createShellSessionDrawer } from "./shell-session-drawer.ts";

function drawer(
  initial: { projectId: string; sessionId: string } | null = { projectId: "p", sessionId: "s1" },
  recentsList: Array<{ projectId: string; sessionId: string; title: string }> = [],
) {
  const [chat, setChat] = createSignal(initial);
  const appStore = createAppStore();
  const revealConversation = vi.fn(async () => true);
  const resumeSession = vi.fn(async (row: { projectId: string; sessionId: string }) => {
    setChat({ projectId: row.projectId, sessionId: row.sessionId });
  });
  const navigateFromSearch = vi.fn(async () => undefined);
  const revealFromSearch = vi.fn();
  let dispose!: () => void;
  const value = createRoot((d) => {
    dispose = d;
    const created = createShellSessionDrawer({
      appStore,
      recents: { state: { recents: recentsList } } as never,
      activeChat: chat,
      revealConversation,
      resumeSession: resumeSession as never,
      navigateFromSearch: navigateFromSearch as never,
      revealFromSearch,
    });
    created.observeActiveChat();
    return created;
  });
  return { value, appStore, setChat, revealConversation, resumeSession, navigateFromSearch, revealFromSearch, dispose };
}

describe("session drawer", () => {
  it("shows one thing at a time: the worklog or a worker", () => {
    const { value, appStore, dispose } = drawer();
    appStore.actions.setWorkers([
      { id: "w-default", parent_session_id: "s1", project_id: "p", agent_type: "implementer", status: "running", created_at: "2026-01-01T00:00:00Z" },
    ]);
    expect(value.drawerContent()).toBeNull();
    value.openWorklog();
    expect(value.worklogOpen()).toBe(true);
    value.openWorkers();
    expect(value.drawerContent()).toBe("worker");
    expect(value.selectedWorkerId()).toBe("w-default");
    value.openWorkers("w1", { scrollTo: "evidence" });
    expect(value.workerDrawerFocus()).toBe("evidence");
    value.acknowledgeDrawerFocus();
    expect(value.workerDrawerFocus()).toBeNull();
    value.closeDrawer();
    expect(value.drawerContent()).toBeNull();
    dispose();
  });

  it("stays closed without a session and closes when the session changes", async () => {
    const none = drawer(null);
    none.value.openWorklog();
    none.value.openWorkers("w1");
    expect(none.value.drawerContent()).toBeNull();
    none.dispose();

    const { value, setChat, dispose } = drawer();
    value.openWorklog();
    setChat({ projectId: "p", sessionId: "s2" });
    await Promise.resolve();
    expect(value.drawerContent()).toBeNull();
    dispose();
  });

  it("resumes direct recent sessions and worker child notifications", async () => {
    const { value, appStore, resumeSession, dispose } = drawer(
      { projectId: "p", sessionId: "s1" },
      [{ projectId: "p", sessionId: "s-direct", title: "Direct" }],
    );
    value.openNotificationSession("  ");
    expect(resumeSession).not.toHaveBeenCalled();

    value.openNotificationSession("s-direct");
    expect(resumeSession).toHaveBeenCalledWith({ projectId: "p", sessionId: "s-direct", title: "Direct" });

    appStore.actions.setWorkers([
      {
        id: "job-child",
        child_session_id: "child-sess",
        parent_session_id: "s-parent",
        project_id: "p",
        agent_type: "implementer",
        status: "complete",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);
    value.openNotificationSession("child-sess");
    await Promise.resolve();
    await new Promise((r) => queueMicrotask(() => r(undefined)));
    expect(value.drawerContent()).toBe("worker");
    expect(value.selectedWorkerId()).toBe("job-child");
    dispose();
  });

  it("reveals transcript anchors and opens worker drawers on search navigation", async () => {
    const { value, appStore, navigateFromSearch, revealFromSearch, dispose } = drawer({
      projectId: "p",
      sessionId: "s1",
    });
    value.revealInTranscript(null, { sessionId: "s1" } as never);
    expect(navigateFromSearch).not.toHaveBeenCalled();

    value.revealInTranscript("p", {
      sessionId: "s1",
      checkpointId: "cp-missing",
      anchor: { messageId: "m1" } as never,
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(revealFromSearch).toHaveBeenCalled();

    appStore.actions.setWorkers([
      {
        id: "w-search",
        parent_session_id: "s1",
        project_id: "p",
        agent_type: "implementer",
        status: "running",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);
    value.revealInTranscript("p", {
      sessionId: "s1",
      workerId: "w-search",
    } as never);
    await new Promise((r) => setTimeout(r, 0));
    expect(value.workersOpen()).toBe(true);
    expect(value.selectedWorkerId()).toBe("w-search");
    dispose();
  });
});
