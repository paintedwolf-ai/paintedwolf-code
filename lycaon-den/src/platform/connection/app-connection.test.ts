import { installConnectionFixtureCleanup, reconcileActiveScope, reconcileRecentsWithStores, fetchHealth, noteStoreRevision, discoverBackend, createLycaonClient, subscribeEvents, resetSessionEventRevisions, refreshPreflight, getHost, lastSubscribeOptions, closeMock, registeredShell, loadModule } from "./app-connection-test-fixture.ts";

import { stubClient } from "../../test/client-fixture.ts";

import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";

import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";

import { createShellStore } from "../../store/shell-store.ts";

import { wireProject } from "../../api/mocks/project-fixture.ts";

import { hostIdentity } from "./host-identity.ts";
import { testHostInfo } from "./host-identity-test.ts";

installConnectionFixtureCleanup();
describe("app connection cache", () => {
  it("replaces the event stream when cached backend verification finds a new store", async () => {
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    expect(subscribeEvents).toHaveBeenCalledOnce();
    closeMock.mockClear();
    resetSessionEventRevisions.mockClear();
    noteStoreRevision.mockReturnValue(true);
    client.listProjects.mockImplementation(async () => {
      expect(closeMock).toHaveBeenCalledOnce();
      expect(resetSessionEventRevisions).toHaveBeenCalledWith(appStore.actions);
      return [];
    });

    await mod.connectAppBackend(appStore);

    expect(discoverBackend).toHaveBeenCalledOnce();
    expect(subscribeEvents).toHaveBeenCalledTimes(2);
  });

  it("reloads file-summary settings after cached verification resets the store", async () => {
    const appStore = createAppStore();
    const client = stubClient({
      listProjects: vi.fn(async () => []),
      getFileSummariesSettings: vi.fn().mockResolvedValue({ enabled: false }),
    });
    createLycaonClient.mockReturnValue(client);
    const mod = await loadModule();
    const setting = await import("../../settings/editor/file-summary-settings.ts");
    await mod.connectAppBackend(appStore);
    await vi.waitFor(() => expect(setting.fileSummariesSettingKnown()).toBe(true));
    expect(setting.fileSummariesEnabled()).toBe(false);
    vi.mocked(client.getFileSummariesSettings).mockClear();
    const preflightReads = refreshPreflight.mock.calls.length;
    noteStoreRevision.mockReturnValue(true);

    await mod.connectAppBackend(appStore);

    await vi.waitFor(() => expect(client.getFileSummariesSettings).toHaveBeenCalledOnce());
    expect(refreshPreflight).toHaveBeenCalledTimes(preflightReads + 1);
    expect(setting.fileSummariesSettingKnown()).toBe(true);
    expect(setting.fileSummariesEnabled()).toBe(false);
  });

  it("reconciles recents and active session when store revision changes on connect", async () => {
    noteStoreRevision.mockReturnValue(true);
    const appStore = createAppStore();
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
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    const recents = createRecentsStore();
    mod.registerRecentsStore(recents);
    await mod.connectAppBackend(appStore);

    expect(reconcileRecentsWithStores).toHaveBeenCalledWith(
      client,
      recents,
      registeredShell,
      expect.any(Function),
    );
    expect(reconcileActiveScope).toHaveBeenCalledWith(appStore, client, expect.anything(), {
      onSessionGone: expect.any(Function),
      resumeVisible: true,
      shouldApply: expect.any(Function),
    });
  });

  it("reconciles recents even when no session is active", async () => {
    noteStoreRevision.mockReturnValue(true);
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    const recents = createRecentsStore();
    mod.registerRecentsStore(recents);
    await mod.connectAppBackend(appStore);

    expect(reconcileRecentsWithStores).toHaveBeenCalledWith(
      client,
      recents,
      registeredShell,
      expect.any(Function),
    );
    expect(reconcileActiveScope).not.toHaveBeenCalled();
  });

  it("rehydrates like a store change when the connected install is a different host", async () => {
    noteStoreRevision.mockReturnValue(false);
    getHost.mockResolvedValue(testHostInfo({ host_id: "00000000-0000-4000-8000-0000000000a2" }));
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    const recents = createRecentsStore();
    mod.registerRecentsStore(recents);
    await mod.connectAppBackend(appStore);

    expect(reconcileRecentsWithStores).toHaveBeenCalledWith(
      client,
      recents,
      registeredShell,
      expect.any(Function),
    );
    expect(hostIdentity()?.host_id).toBe("00000000-0000-4000-8000-0000000000a2");
  });

  it("skips reconcile on first connect when revision is new", async () => {
    noteStoreRevision.mockReturnValue(false);
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    expect(reconcileRecentsWithStores).not.toHaveBeenCalled();
    expect(reconcileActiveScope).not.toHaveBeenCalled();
  });

  it("reconciles recents after SSE reconnect when store revision changed", async () => {
    noteStoreRevision.mockReturnValueOnce(false).mockReturnValueOnce(true);
    fetchHealth.mockResolvedValue({
      status: "ok",
      version: "0.1.0",
      store_revision: 2,
      schema_version: 1,
    });
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    const recents = createRecentsStore();
    mod.registerRecentsStore(recents);
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");

    lastSubscribeOptions?.onOpen?.("reconnect");

    await vi.waitFor(() => {
      expect(reconcileRecentsWithStores).toHaveBeenCalledWith(
        client,
        recents,
        registeredShell,
        expect.any(Function),
      );
    });
  });

  it("rehydrates after SSE reconnect reaches a different host", async () => {
    noteStoreRevision.mockReturnValue(false);
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    const recents = createRecentsStore();
    mod.registerRecentsStore(recents);
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");
    expect(reconcileRecentsWithStores).not.toHaveBeenCalled();

    getHost.mockResolvedValue(testHostInfo({ host_id: "00000000-0000-4000-8000-0000000000a3" }));
    lastSubscribeOptions?.onOpen?.("reconnect");

    await vi.waitFor(() => {
      expect(reconcileRecentsWithStores).toHaveBeenCalledWith(
        client,
        recents,
        registeredShell,
        expect.any(Function),
      );
    });
  });

  it("reconciles after SSE reconnect when a session is active and revision is unchanged", async () => {
    noteStoreRevision.mockReturnValue(false);
    fetchHealth.mockResolvedValue({
      status: "ok",
      version: "0.1.0",
      store_revision: 1,
      schema_version: 1,
    });
    const appStore = createAppStore();
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
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");

    lastSubscribeOptions?.onOpen?.("reconnect");

    await vi.waitFor(() => {
      expect(reconcileActiveScope).toHaveBeenCalledWith(appStore, client, expect.anything(), {
        onSessionGone: expect.any(Function),
        resumeVisible: true,
      shouldApply: expect.any(Function),
      });
    });
    expect(reconcileRecentsWithStores).not.toHaveBeenCalled();
  });

  it("clears shell scope when connect loads an empty registry", async () => {
    const appStore = createAppStore();
    const shell = createShellStore("connected");
    shell.commitStageScope({
      projectId: "gone-project",
      sessionId: "sess-1",
    });

    const mod = await loadModule();
    mod.registerShellStore(shell);
    mod.registerProjectsStore(
      createProjectsStore(() => null, { onProjectEvent: () => () => {} }),
    );
    await mod.connectAppBackend(appStore);

    expect(shell.state.activeProjectId).toBeNull();
    expect(shell.state.foreground).toBeNull();
    expect(appStore.state.currentSession).toBeUndefined();
  });

  it("reconciles an active shell project omitted from a stale listProjects snapshot", async () => {
    const appStore = createAppStore();
    const shell = createShellStore("connected");
    const project = wireProject("/tmp/draft", "draft-1");
    shell.commitStageScope({ projectId: project.id, sessionId: "sess-1" });
    const client = {
      listProjects: vi.fn(async () => []),
      getProject: vi.fn(async () => project),
    };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    mod.registerShellStore(shell);
    const projects = createProjectsStore(() => client as never, {
      onProjectEvent: () => () => {},
    });
    mod.registerProjectsStore(projects);
    await mod.connectAppBackend(appStore);

    expect(client.getProject).toHaveBeenCalledWith("draft-1");
    expect(projects.byId("draft-1")).toEqual(project);
    expect(shell.state.activeProjectId).toBe("draft-1");
    expect(shell.state.foreground?.sessionId).toBe("sess-1");
  });

  it("reconnects when the warm client fails project sync after a sidecar restart", async () => {
    const appStore = createAppStore();
    const listProjects = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error("401 unauthorized"))
      .mockResolvedValueOnce([]);
    const client = { listProjects };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    await mod.connectAppBackend(appStore);

    expect(listProjects).toHaveBeenCalledTimes(3);
    expect(discoverBackend).toHaveBeenCalledTimes(2);
  });

  it("rehydrates the selected chat after rediscovery without a store revision change", async () => {
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    appStore.actions.setCurrentSession({
      id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "p1", workspace_path: "/tmp/p", posture: "build", status: "busy",
      created_at: "t", activity_at: "t", updated_at: "t",
    });
    client.listProjects.mockRejectedValueOnce(new Error("old engine stopped"));
    reconcileActiveScope.mockClear();
    await mod.connectAppBackend(appStore);
    expect(discoverBackend).toHaveBeenCalledTimes(2);
    expect(reconcileActiveScope).toHaveBeenCalledWith(appStore, client, expect.anything(), expect.objectContaining({ resumeVisible: true }));
  });

  it("re-verifies a warm client before returning it", async () => {
    const appStore = createAppStore();
    const listProjects = vi.fn(async () => []);
    createLycaonClient.mockReturnValue({ listProjects });
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    listProjects.mockClear();
    await mod.connectAppBackend(appStore);
    expect(listProjects).toHaveBeenCalledTimes(1);
  });

});
