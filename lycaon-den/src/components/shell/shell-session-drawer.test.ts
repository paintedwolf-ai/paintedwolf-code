import { createRoot, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { createShellSessionDrawer } from "./shell-session-drawer.ts";

function drawer(initial: { projectId: string; sessionId: string } | null = { projectId: "p", sessionId: "s1" }) {
  const [chat, setChat] = createSignal(initial);
  let dispose!: () => void;
  const value = createRoot((d) => {
    dispose = d;
    const created = createShellSessionDrawer({
      appStore: createAppStore(), recents: { state: { recents: [] } } as never, activeChat: chat,
      revealConversation: vi.fn(async () => true), resumeSession: vi.fn(async () => undefined) as never,
      navigateFromSearch: vi.fn(async () => undefined) as never, revealFromSearch: vi.fn(),
    });
    created.observeActiveChat();
    return created;
  });
  return { value, setChat, dispose };
}

describe("session drawer", () => {
  it("shows one thing at a time: the worklog or a worker", () => {
    const { value, dispose } = drawer();
    expect(value.drawerContent()).toBeNull();
    value.openWorklog();
    expect(value.worklogOpen()).toBe(true);
    value.openWorkers("w1");
    expect(value.drawerContent()).toBe("worker");
    expect(value.worklogOpen()).toBe(false);
    expect(value.selectedWorkerId()).toBe("w1");
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
});
