// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { createSessionNavigation } from "./session-navigation.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { LycaonApiError } from "../../api/http.ts";
const seams = vi.hoisted(() => ({ update: vi.fn(), hydrate: vi.fn(), report: vi.fn(), retire: vi.fn() }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => ({ updateSession: seams.update }),
  getEntityRetire: () => ({ session: seams.retire }), noticeReporterFor: () => ({ reportError: seams.report }),
  projectEventsAreSubscribed: () => true, subscribeProjectEvents: vi.fn(), waitForProjectEvents: vi.fn(async () => true) }));
vi.mock("../../chat/session/session-lifecycle.ts", async (original) => ({ ...(await original<typeof import("../../chat/session/session-lifecycle.ts")>()), resumeChatSession: seams.hydrate }));

function mountNavigation() {
  const shell = createShellStore("connected"); const appStore = createAppStore();
  shell.commitStageScope({ projectId: "project", sessionId: "session" });
  appStore.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle", title: "Original" } as never);
  appStore.actions.installTranscriptBaseline("session", [], 0);
  const recents = { state: { recents: [] }, registerSession: vi.fn(async () => undefined) };
  const projectSessions = { state: { projectId: "project", rows: [], sort: "recent" }, setProject: vi.fn(), setSort: vi.fn(), refresh: vi.fn(async () => undefined) };
  const calls = { showProjects: vi.fn(), closeSearch: vi.fn(), closeCrossbar: vi.fn(), closeLauncher: vi.fn(),
    revealConversation: vi.fn(async () => true), focusComposer: vi.fn() };
  const navigation = createRoot((dispose) => {
    onTestFinished(dispose);
    return createSessionNavigation({ shell, appStore, recents: recents as never, projectSessions: projectSessions as never,
      projects: { state: { projects: [] }, byId: () => ({ roots: [] }) } as never,
      activeChat: () => shell.state.foreground ? { projectId: shell.state.foreground.projectId, sessionId: shell.state.foreground.sessionId } : null,
      nav: () => "projects", conversationInLayout: () => false, ...calls });
  });
  return { shell, appStore, recents, projectSessions, calls, navigation };
}

beforeEach(() => vi.clearAllMocks());
describe("shell session navigation", () => {
  it("publishes renamed host titles and scopes mutation failures", async () => {
    const { navigation, appStore, recents, projectSessions } = mountNavigation();
    seams.update.mockResolvedValueOnce({ title: "  Host title  " });
    await navigation.renameSession({ projectId: "project", sessionId: "session" }, "Requested");
    expect(appStore.state.currentSession?.title).toBe("Host title");
    expect(recents.registerSession).toHaveBeenCalledWith({ projectId: "project", sessionId: "session", title: "Host title" });
    expect(projectSessions.refresh).toHaveBeenCalledOnce();
    const error = new Error("Rename failed"); seams.update.mockRejectedValueOnce(error);
    await navigation.renameSession({ projectId: "project", sessionId: "session" }, "Failed");
    expect(seams.report).toHaveBeenCalledWith(error);
    expect(appStore.state.currentSession?.title).toBe("Host title");
  });

  it("runs same-project and cross-project hydration through the session switch service", async () => {
    const { navigation, shell, appStore } = mountNavigation();
    seams.hydrate.mockImplementation(async (store: ReturnType<typeof createAppStore>, _client, sessionId: string, _projects, _recents, options: { projectId: string }) => {
      store.actions.setCurrentSession({ id: sessionId, project_id: options.projectId, status: "idle" } as never);
      store.actions.installTranscriptBaseline(sessionId, [], 0);
    });
    await navigation.resumeSession({ projectId: "project", sessionId: "second", title: "Second" });
    expect(shell.state.foreground?.sessionId).toBe("second"); expect(appStore.state.chatHydrationLock).toBeUndefined();
    await navigation.resumeSession({ projectId: "other", sessionId: "third", title: "Third" });
    expect(shell.state.activeProjectId).toBe("other"); expect(appStore.state.currentSession?.id).toBe("third");
    expect(appStore.state.chatHydrationLock).toBeUndefined();
    seams.hydrate.mockRejectedValueOnce(new LycaonApiError("gone", 404, "session_not_found"));
    await navigation.resumeSession({ projectId: "other", sessionId: "gone", title: "Gone" });
    expect(seams.retire).toHaveBeenCalledWith({ projectId: "other", sessionId: "gone" });
    expect(shell.state.activeProjectId).toBeNull(); expect(appStore.state.currentSession).toBeUndefined();
    expect(seams.report).toHaveBeenCalled();
  });

  it("preserves opening tokens and focuses an already fresh chat", async () => {
    const { navigation, shell, calls } = mountNavigation();
    await navigation.startNewChat("project");
    expect(calls.focusComposer).toHaveBeenCalledOnce();
    const token = navigation.beginWorkspaceOpening("Garden");
    navigation.abandonWorkspaceOpening(token + 1); expect(shell.state.opening?.token).toBe(token);
    navigation.abandonWorkspaceOpening(token); expect(shell.state.opening).toBeNull();
    expect(calls.showProjects).toHaveBeenCalled();
  });
});
