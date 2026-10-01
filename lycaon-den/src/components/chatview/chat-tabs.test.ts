// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createChatTabsState } from "./chat-tabs.ts";
import type { ChatTabChromeValue } from "../../chat/composer/chat-tab-chrome.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { type AppStore } from "../../store/app-state-model.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { WorkflowRun } from "../../api/types.ts";

const connection = vi.hoisted(() => ({ client: null as LycaonClient | null }));
vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => connection.client,
}));
afterEach(() => { connection.client = null; });

function chromeStub(
  open: () => "workflows" | null = () => null,
): ChatTabChromeValue {
  return {
    openTab: open,
    panelRetracted: () => false,
    tabRefocus: () => 0,
    selectTab: vi.fn(),
    setPanelRetracted: vi.fn(),
    syncPanelHeight: vi.fn(),
    panelHeightPx: () => 0,
  };
}

describe("createChatTabsState", () => {
  it("refreshes on navigation and reconnect, never on the data its own response writes", async () => {
    const store = createAppStore();
    const [sessionId, setSessionId] = createSignal("sess-1");
    const [projectDir, setProjectDir] = createSignal("/repo");
    const [open, setOpen] = createSignal<"workflows" | null>("workflows");
    const [offline, setOffline] = createSignal(false);
    const setSession = (id: string) => store.actions.setCurrentSession({
      id, owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    });
    setSession(sessionId());
    const pending: Array<(run: WorkflowRun | undefined) => void> = [];
    const getActiveWorkflowRun = vi.fn(() => new Promise<WorkflowRun | undefined>((resolve) => pending.push(resolve)));
    connection.client = stubClient({
      getActiveWorkflowRun,
      listSessionWorkflowRuns: async () => ({ runs: [] }),
      listWorkflows: async () => [],
    });
    const dispose = createRoot((dispose) => {
      createChatTabsState({
        chrome: chromeStub(open), appStore: store, sessionId, projectDir, offline, projects: emptyProjects,
      });
      return dispose;
    });
    const completeRefresh = async (count: number) => {
      expect(getActiveWorkflowRun).toHaveBeenCalledTimes(count);
      const epoch = store.state.workflowEventEpoch;
      pending.shift()!(undefined);
      await vi.waitFor(() => expect(store.state.workflowEventEpoch).toBe(epoch + 1));
      expect(getActiveWorkflowRun).toHaveBeenCalledTimes(count);
    };
    try {
      await completeRefresh(1);
      // A later snapshot also changes the revision without changing navigation.
      store.actions.setWorkflowState(sessionId(), store.state.sessionViewEpoch, {
        activeWorkflowRun: undefined, workflowRuns: [], workflowCatalog: [], blueprints: [],
      });
      expect(getActiveWorkflowRun).toHaveBeenCalledTimes(1);
      setOpen(null);
      setOffline(true);
      setOpen("workflows");
      expect(getActiveWorkflowRun).toHaveBeenCalledTimes(1);
      setOffline(false);
      await completeRefresh(2);
      setOpen(null);
      setOpen("workflows");
      await completeRefresh(3);
      setProjectDir("/another-repo");
      await completeRefresh(4);
      setSession("sess-2");
      setSessionId("sess-2");
      await completeRefresh(5);
      expect(getActiveWorkflowRun).toHaveBeenLastCalledWith("sess-2");
    } finally {
      dispose();
    }
  });

  it("tracks workflows tab open state from chrome", () => {
    createRoot((dispose) => {
      const tabs = createChatTabsState({
        chrome: chromeStub(() => "workflows"),
        appStore: { state: {} } as AppStore,
        sessionId: () => "sess-1",
        projectDir: () => "/repo",
        offline: () => true,
        projects: emptyProjects,
      });
      expect(tabs.workflowsOpen()).toBe(true);
      dispose();
    });
  });

  it("selects workflows tab when setWorkflowsOpen(true)", () => {
    createRoot((dispose) => {
      const chrome = chromeStub();
      const tabs = createChatTabsState({
        chrome,
        appStore: { state: {} } as AppStore,
        sessionId: () => "sess-1",
        projectDir: () => "/repo",
        offline: () => true,
        projects: emptyProjects,
      });
      tabs.setWorkflowsOpen(true);
      expect(chrome.selectTab).toHaveBeenCalledWith("workflows");
      dispose();
    });
  });
});
