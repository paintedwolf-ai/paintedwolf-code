// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { describe, expect, it, onTestFinished, vi } from "vitest";

const creation = vi.hoisted(() => ({ start: vi.fn(async (): Promise<void> => undefined), opened: true }));
vi.mock("./session-creation.ts", async (original) => ({
  ...(await original<typeof import("./session-creation.ts")>()),
  createSessionCreation: () => creation.start,
}));

import { createSessionNavigation } from "./session-navigation.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createShellStore } from "../../store/shell-store.ts";

function mount() {
  const calls = { showProjects: vi.fn(), closeSearch: vi.fn(), closeCrossbar: vi.fn(), closeLauncher: vi.fn(),
    revealConversation: vi.fn(async () => true), focusComposer: vi.fn() };
  const shell = createShellStore("connected");
  const navigation = createRoot((dispose) => {
    onTestFinished(dispose);
    return createSessionNavigation({ shell, appStore: createAppStore(),
      recents: { state: { recents: [] }, registerSession: vi.fn() } as never,
      projectSessions: { state: { projectId: null, rows: [], sort: "recent" }, setProject: vi.fn(), setSort: vi.fn(), refresh: vi.fn(async () => undefined) } as never,
      projects: { state: { projects: [] }, byId: () => undefined } as never,
      activeChat: () => (creation.opened ? { projectId: "p", sessionId: "s" } : null), nav: () => "projects", conversationInLayout: () => false, ...calls });
  });
  return { navigation, calls };
}

describe("new project", () => {
  it("focuses the new chat's composer once the project exists", async () => {
    const { navigation, calls } = mount();
    creation.opened = true;
    let finish!: () => void;
    creation.start.mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
    navigation.newProject();
    expect(creation.start).toHaveBeenCalledWith({ kind: "new-project" });
    expect(calls.focusComposer).not.toHaveBeenCalled();
    finish();
    await Promise.resolve();
    await Promise.resolve();
    expect(calls.focusComposer).toHaveBeenCalledOnce();
  });

  it("leaves focus alone when creation returned Home", async () => {
    const { navigation, calls } = mount();
    creation.opened = false;
    navigation.newProject();
    await Promise.resolve();
    await Promise.resolve();
    expect(calls.focusComposer).not.toHaveBeenCalled();
  });
});
