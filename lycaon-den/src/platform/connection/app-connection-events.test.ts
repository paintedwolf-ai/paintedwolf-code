import { installConnectionFixtureCleanup, fetchHealth, noteHealthResponse, watchWindowExit, reconcileActiveScope, refreshCodeScanCache, noteStoreRevision, createLycaonClient, subscribeEvents, resetSessionEventRevisions, refreshPreflight, lastSubscribeOptions, lastSubscribeHandlers, subscribedProjectIds, closeMock, loadModule, testEntityRetire } from "./app-connection-test-fixture.ts";

import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { createAppStore } from "../../store/app-state.ts";

import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";

import { createShellStore } from "../../store/shell-store.ts";

import { wireProject } from "../../api/mocks/project-fixture.ts";

import { connectSourceTreeWorkspace } from "../../files/tree/source-tree-store.ts";

installConnectionFixtureCleanup();
describe("app connection events", () => {
  it.each(["revision", "reconnect", "event"])("ignores a delayed %s scope failure from a replaced backend", async mode => {
    const mod = await loadModule();
    const appStore = createAppStore();
    let rejectRead!: (error: Error) => void;
    const client = {
      listProjects: vi.fn(async () => []),
      getSessionBootstrap: vi.fn(() => new Promise((_resolve, reject) => { rejectRead = reject; })),
    };
    createLycaonClient.mockReturnValue(client);
    await mod.connectAppBackend(appStore);
    appStore.actions.setCurrentSession({
      id: "session-kept", project_id: "project-kept", workspace_path: "/tmp/p",
      owner_person_id: "00000000-0000-4000-8000-000000000002", posture: "build",
      status: "idle", created_at: "t", activity_at: "t", updated_at: "t",
    });
    const actual = await vi.importActual<typeof import("../../chat/session/session-reconcile.ts")>("../../chat/session/session-reconcile.ts");
    reconcileActiveScope.mockImplementation(actual.reconcileActiveScope);
    noteStoreRevision.mockReturnValue(mode === "revision");
    const settled = mode === "event"
      ? lastSubscribeOptions!.onReconcile!().catch(() => undefined)
      : (lastSubscribeOptions!.onOpen!("reconnect"), Promise.resolve());
    await vi.waitFor(() => expect(client.getSessionBootstrap).toHaveBeenCalledOnce());
    createLycaonClient.mockReturnValue({ listProjects: vi.fn(async () => []) });
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:9992", apiToken: "replacement" });
    const reset = vi.spyOn(appStore.actions, "resetChatForSessionSwitch");
    rejectRead(new LycaonApiError("Missing from the old host", 404, "session_not_found"));
    await settled;
    await new Promise<void>(resolve => setImmediate(resolve));
    expect(reset).not.toHaveBeenCalled();
    expect(appStore.state.currentSession?.id).toBe("session-kept");
  });

  it("ignores reconnect health after its backend was replaced", async () => {
    const mod = await loadModule();
    const appStore = createAppStore();
    const oldClient = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(oldClient);
    await mod.connectAppBackend(appStore);
    let resolveHealth!: (value: { status: string; version: string; store_revision: number; schema_version: number }) => void;
    fetchHealth.mockImplementationOnce(() => new Promise(resolve => { resolveHealth = resolve; }));
    lastSubscribeOptions?.onOpen?.("reconnect");
    expect(resolveHealth).toBeDefined();
    createLycaonClient.mockReturnValue({ listProjects: vi.fn(async () => []) });
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:9992", apiToken: "replacement" });
    noteStoreRevision.mockClear();
    noteHealthResponse.mockClear();
    resolveHealth({ status: "recovery", version: "0.1.0", store_revision: 999, schema_version: 1 });
    await new Promise<void>(resolve => setImmediate(resolve));
    expect(noteStoreRevision).not.toHaveBeenCalled();
    expect(noteHealthResponse).not.toHaveBeenCalled();
  });

  it("closes the old event stream before attaching a replacement backend", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    const order: string[] = [];
    closeMock.mockImplementation(async () => { order.push("close"); });
    resetSessionEventRevisions.mockImplementation(() => { order.push("reset"); });
    createLycaonClient.mockImplementation(() => {
      order.push("client");
      return { listProjects: vi.fn(async () => []) };
    });

    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8788", apiToken: "replacement" });

    expect(order).toEqual(["close", "reset", "client"]);
    expect(resetSessionEventRevisions).toHaveBeenLastCalledWith(appStore.actions);
  });

  it("keeps an attached home window subscribed for engine recovery", async () => {
    const mod = await loadModule();
    const appStore = createAppStore();
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8788", apiToken: "attached" });
    await vi.waitFor(() => expect(subscribeEvents).toHaveBeenCalledOnce());
    expect(subscribedProjectIds).toEqual([""]);
    lastSubscribeOptions?.onReconnectAttempt?.(1, 0);
    appStore.actions.setSidecarStatus("disconnected");
    lastSubscribeOptions?.onOpen?.("reconnect");
    expect(appStore.state.sidecarStatus).toBe("connected");
  });

  it("subscribes device-wide when no project is foreground", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();

    await mod.connectAppBackend(appStore);

    expect(subscribedProjectIds).toEqual([""]);
  });

  it("refreshes global provider readiness without a foreground workspace", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    refreshPreflight.mockClear();

    lastSubscribeOptions?.onInvalidate?.(["providers"], { kind: "device" });

    expect(refreshPreflight).toHaveBeenCalledOnce();
  });

  it("does not apply a background session invalidation to the foreground view", async () => {
    const getSessionFindings = vi.fn();
    createLycaonClient.mockReturnValue({
      listProjects: vi.fn(async () => []),
      getSessionFindings,
    });
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "session-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);

    lastSubscribeHandlers?.findings?.(
      { revision: 2 },
      { kind: "session", project_id: "proj-1", session_id: "session-b" },
    );

    expect(getSessionFindings).not.toHaveBeenCalled();
  });

  it("applies project source changes from a background session", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "session-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");
    const connection = connectSourceTreeWorkspace({
      projectId: "proj-1",
      workspaceId: "workspace-b",
      browse: async (rootId, dir) => ({
        workspace_id: "workspace-b", root_id: rootId, dir,
        watch_complete: true, entries: [],
      }),
    });
    await connection.load("r1", ".");

    lastSubscribeHandlers?.source_changed?.(
      {
        project_id: "proj-1",
        workspace_id: "workspace-b",
        workspace_kind: "project",
        resync: false,
        changes: [{
          session_id: "session-b",
          root_id: "r1",
          path: "agent-created.ts",
          op: "create",
          origin: "agent",
          is_dir: false,
          changed_at: "2026-08-22T00:00:00Z",
        }],
      },
      { kind: "session", project_id: "proj-1", session_id: "session-b" },
    );
    expect(connection.get("r1", ".")?.listing.entries).toEqual([
      { name: "agent-created.ts", is_dir: false },
    ]);
    connection.disconnect();
  });

  it("revalidates cached source projections on initial stream open and resume", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");
    const connection = connectSourceTreeWorkspace({
      projectId: "proj-1",
      workspaceId: "workspace-a",
      browse: async (rootId, dir) => ({
        workspace_id: "workspace-a", root_id: rootId, dir,
        watch_complete: true, entries: [],
      }),
    });
    await connection.load("r1", ".");

    lastSubscribeOptions?.onOpen?.("initial");
    expect(connection.get("r1", ".")?.stale).toBe(true);
    await connection.load("r1", ".", true);
    expect(connection.get("r1", ".")?.stale).toBe(false);
    lastSubscribeOptions?.onOpen?.("resume");

    expect(connection.get("r1", ".")?.stale).toBe(true);
    connection.disconnect();
  });

  it("refreshes scan cache on scan invalidation", async () => {
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });

    lastSubscribeOptions?.onInvalidate?.(["scan"], {
      kind: "session",
      project_id: "00000000-0000-4000-8000-000000000001",
      session_id: "sess-1",
    });

    await vi.waitFor(() => {
      expect(refreshCodeScanCache).toHaveBeenCalledWith(appStore, client);
    });
  });

  it("onSessionGone hook removes recent and evicts shell foreground", async () => {
    noteStoreRevision.mockReturnValue(true);
    let capturedOnSessionGone:
      | ((scope: { projectId: string; sessionId: string }) => Promise<void>)
      | undefined;
    reconcileActiveScope.mockImplementation(async (_store, _client, _projects, opts) => {
      capturedOnSessionGone = opts?.onSessionGone;
    });

    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "gone",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const recents = createRecentsStore();
    await recents.registerSession({
      projectId: "00000000-0000-4000-8000-000000000001",
      sessionId: "gone",
      title: "Old chat",
    });
    const shell = createShellStore("connected");
    shell.commitStageScope({
      projectId: "00000000-0000-4000-8000-000000000001",
      sessionId: "gone",
    });
    const project = wireProject(
      "/tmp/p",
      "00000000-0000-4000-8000-000000000001",
    );
    const client = {
      listProjects: vi.fn(async () => []),
      getProject: vi.fn(async () => project),
    };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    mod.registerRecentsStore(recents);
    mod.registerShellStore(shell);
    mod.registerEntityRetire(testEntityRetire(recents));
    mod.registerProjectsStore(
      createProjectsStore(() => null, { onProjectEvent: () => () => {} }),
    );
    await mod.connectAppBackend(appStore);

    expect(capturedOnSessionGone).toBeDefined();
    await capturedOnSessionGone!({
      projectId: "00000000-0000-4000-8000-000000000001",
      sessionId: "gone",
    });

    await vi.waitFor(() => {
      expect(recents.state.recents).toHaveLength(0);
    });
    expect(shell.state.foreground).toBeNull();
  });

  it("uses deleted session events to retire peer-window state", async () => {
    const appStore = createAppStore();
    const recents = createRecentsStore();
    const shell = createShellStore("connected");
    const mod = await loadModule();
    mod.registerRecentsStore(recents);
    mod.registerShellStore(shell);
    mod.registerEntityRetire(testEntityRetire(recents));
    await mod.connectAppBackend(appStore);
    await recents.registerSession({
      projectId: "proj-remote",
      sessionId: "sess-deleted",
      title: "Deleted elsewhere",
    });
    shell.commitStageScope({
      projectId: "proj-remote",
      sessionId: "sess-deleted",
    });

    lastSubscribeHandlers?.session?.(
      {
        id: "sess-deleted",
        project_id: "proj-remote",
        action: "deleted",
        status: "idle",
      },
      {
        kind: "session",
        project_id: "proj-remote",
        session_id: "sess-deleted",
      },
    );

    await vi.waitFor(() => {
      expect(recents.state.recents).toHaveLength(0);
      expect(shell.state.foreground).toBeNull();
    });
  });

  it("resubscribes SSE when shell activeProjectId already matches a new project", async () => {
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);
    const shell = createShellStore("connected");
    shell.commitStageScope({ projectId: "proj-old", sessionId: "sess-old" });

    const mod = await loadModule();
    mod.registerShellStore(shell);
    await mod.connectAppBackend(appStore);

    mod.subscribeProjectEvents(appStore, "proj-old");
    expect(subscribedProjectIds).toEqual(["proj-old"]);

    shell.commitStageScope({ projectId: "proj-new", sessionId: "sess-new" });
    mod.subscribeProjectEvents(appStore, "proj-new");

    expect(subscribedProjectIds).toEqual(["proj-old", "proj-new"]);
    expect(closeMock).toHaveBeenCalledTimes(1);
  });

  it("skips duplicate subscribeProjectEvents for the same project", async () => {
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    mod.registerShellStore(createShellStore("connected"));
    await mod.connectAppBackend(appStore);

    mod.subscribeProjectEvents(appStore, "proj-1");
    mod.subscribeProjectEvents(appStore, "proj-1");

    expect(subscribedProjectIds).toEqual(["", "proj-1"]);
    expect(closeMock).toHaveBeenCalledTimes(1);
    expect(mod.projectEventsAreSubscribed("proj-1")).toBe(true);
    expect(mod.projectEventsAreSubscribed("proj-other")).toBe(false);
  });

  it("awaits the current event stream at native close and reconnects if kept open", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");
    const hooks = watchWindowExit.mock.calls[watchWindowExit.mock.calls.length - 1]?.[1] as {
      beforeDestroy: () => Promise<void>;
      onKeepOpen: () => void;
    };
    let finish!: () => void;
    closeMock.mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
    let completed = false;
    const closing = hooks.beforeDestroy().then(() => { completed = true; });
    await Promise.resolve();
    expect(completed).toBe(false);
    expect(mod.projectEventsAreSubscribed("proj-1")).toBe(false);
    finish();
    await closing;
    hooks.onKeepOpen();
    expect(mod.projectEventsAreSubscribed("proj-1")).toBe(true);
    expect(subscribedProjectIds).toEqual(["", "proj-1", "proj-1"]);
    mod.subscribeProjectEvents(appStore, "proj-2");
    hooks.onKeepOpen();
    expect(mod.projectEventsAreSubscribed("proj-2")).toBe(true);
  });

});
