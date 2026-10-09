import { installConnectionFixtureCleanup, discoverBackend, createLycaonClient, subscribeEvents, refreshPreflight, getHost, lastSubscribeOptions, loadModule, loadModuleWithNotices } from "./app-connection-test-fixture.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import { createCostStore } from "../../store/cost-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { CostSummary } from "../../api/types.ts";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";

import { createProjectsStore } from "../../store/projects-store.ts";

import { createShellStore } from "../../store/shell-store.ts";
import { LycaonApiError } from "../../api/http.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";

import { noteBackendReachable, noteBackendUnreachable } from "./request-connectivity.ts";
import { incompatibleHost } from "./host-identity.ts";
import { TEST_HOST_INFO, testHostInfo } from "./host-identity-test.ts";

installConnectionFixtureCleanup();
describe("app connection host", () => {
  it("invalidates pending cost reads when a backend is replaced", async () => {
    const mod = await loadModule();
    const store = createAppStore();
    const costs = createCostStore();
    mod.registerCostStore(costs);
    let answer!: (value: CostSummary) => void;
    const read = costs.refreshSession(stubClient({ getCostSummary: () => new Promise<CostSummary>((resolve) => { answer = resolve; }) }), "session");
    mod.attachKnownBackend(store, { baseUrl: "http://127.0.0.1:8788", apiToken: "replacement" });
    answer({} as CostSummary);
    await read;
    expect(costs.state.session).toBeUndefined();
    expect(costs.state.loading).toBe(false);
  });

  it("keeps a replacement backend when an old discovery fails", async () => {
    const mod = await loadModule();
    const store = createAppStore();
    let rejectDiscovery!: (error: Error) => void;
    discoverBackend.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectDiscovery = reject; }));
    const connecting = mod.connectAppBackend(store);
    const replacement = stubClient({});
    createLycaonClient.mockReturnValueOnce(replacement);
    mod.attachKnownBackend(store, { baseUrl: "http://127.0.0.1:8788", apiToken: "replacement" });
    rejectDiscovery(new Error("Old discovery failed"));
    await expect(connecting).rejects.toThrow("Old discovery failed");
    expect(mod.getLycaonClient()).toBe(replacement);
    expect(store.state.sidecarStatus).toBe("connected");
  });

  it("does not disconnect a replacement while verifying the old cached client", async () => {
    const mod = await loadModule();
    const store = createAppStore();
    let answer!: (projects: never[]) => void;
    const listProjects = vi.fn().mockResolvedValueOnce([]).mockImplementationOnce(() => new Promise<never[]>((resolve) => { answer = resolve; }));
    createLycaonClient.mockReturnValueOnce(stubClient({ listProjects }));
    await mod.connectAppBackend(store);
    const verifying = mod.connectAppBackend(store);
    await vi.waitFor(() => expect(listProjects).toHaveBeenCalledTimes(2));
    const replacement = stubClient({});
    createLycaonClient.mockReturnValueOnce(replacement);
    mod.attachKnownBackend(store, { baseUrl: "http://127.0.0.1:8788", apiToken: "replacement" });
    answer([]);
    await expect(verifying).rejects.toMatchObject({ name: "AbortError" });
    expect(mod.getLycaonClient()).toBe(replacement);
    expect(store.state.sidecarStatus).toBe("connected");
  });

  it.each(["found", "missing"])("ignores an old backend's %s project lookup after replacement", async (result) => {
    const mod = await loadModule();
    const store = createAppStore();
    const shell = createShellStore("connected");
    shell.commitStageScope({ projectId: "old", sessionId: "old-session" });
    mod.registerShellStore(shell);
    const projects = createProjectsStore(mod.getLycaonClient, { onProjectEvent: () => () => {} });
    mod.registerProjectsStore(projects);
    let answer!: (project: ReturnType<typeof wireProject>) => void;
    let reject!: (error: Error) => void;
    const getProject = vi.fn(() => new Promise<ReturnType<typeof wireProject>>((resolve, fail) => { answer = resolve; reject = fail; }));
    createLycaonClient.mockReturnValueOnce(stubClient({ listProjects: async () => [], getProject }));
    const connecting = mod.connectAppBackend(store);
    await vi.waitFor(() => expect(getProject).toHaveBeenCalledOnce());
    const replacement = stubClient({});
    createLycaonClient.mockReturnValueOnce(replacement);
    mod.attachKnownBackend(store, { baseUrl: "http://127.0.0.1:8788", apiToken: "replacement" });
    shell.commitStageScope({ projectId: "new", sessionId: "new-session" });
    if (result === "found") answer(wireProject("/old", "old"));
    else reject(new LycaonApiError("Missing", 404, "project_not_found"));
    await expect(connecting).rejects.toMatchObject({ name: "AbortError" });
    expect(projects.byId("old")).toBeUndefined();
    expect(shell.state.activeProjectId).toBe("new");
    expect(mod.getLycaonClient()).toBe(replacement);
  });

  it("does not install old connection settings after backend replacement", async () => {
    const mod = await loadModule();
    const appStore = createAppStore();
    mod.registerShellStore(createShellStore("connected"));
    mod.registerProjectsStore(createProjectsStore(mod.getLycaonClient, { onProjectEvent: () => () => {} }));
    const settings = createSettingsStore();
    mod.registerSettingsStore(settings);
    let answer!: (providers: never[]) => void;
    const listProviders = vi.fn(() => new Promise<never[]>((resolve) => { answer = resolve; }));
    createLycaonClient.mockReturnValueOnce(stubClient({
      listProjects: async () => [], listProviders,
      listProviderKinds: async () => [], getModelPolicySettings: async () => ({}), getPricingSettings: async () => ({}),
    }));
    const connecting = mod.connectAppBackend(appStore);
    await vi.waitFor(() => expect(listProviders).toHaveBeenCalledOnce());
    const replacement = stubClient({});
    createLycaonClient.mockReturnValueOnce(replacement);
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8788", apiToken: "replacement" });
    const setProviders = vi.spyOn(settings.actions, "setProviders");
    answer([]);
    await expect(connecting).rejects.toMatchObject({ name: "AbortError" });
    expect(setProviders).not.toHaveBeenCalled();
    expect(mod.getLycaonClient()).toBe(replacement);
  });

  it("does not publish notices when bootstrap connect fails", async () => {
    discoverBackend.mockRejectedValue(new TypeError("Load failed"));
    const appStore = createAppStore();
    const { mod, all } = await loadModuleWithNotices();
    await expect(mod.connectAppBackend(appStore)).rejects.toThrow("Load failed");
    expect(all()).toHaveLength(0);
    expect(appStore.state.sidecarStatus).toBe("disconnected");
  });

  it("uses shared HTTP transport observations for window reachability", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    mod.registerAppStore(appStore);
    appStore.actions.setSidecarStatus("connected");

    noteBackendUnreachable();
    expect(appStore.state.sidecarStatus).toBe("disconnected");

    noteBackendReachable();
    expect(appStore.state.sidecarStatus).toBe("connected");
    expect(refreshPreflight).toHaveBeenCalled();
  });

  it("does not turn failed readiness reads into a reachable-request feedback loop", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    mod.registerAppStore(appStore);
    let reads = 0;
    const getFileSummariesSettings = vi.fn(async () => {
      reads++;
      await Promise.resolve();
      if (reads < 5) noteBackendReachable();
      throw new LycaonApiError("Store unavailable", 503, "internal_error");
    });
    createLycaonClient.mockReturnValueOnce(stubClient({ getFileSummariesSettings }));
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8788", apiToken: "fixture" });
    const preflightReads = refreshPreflight.mock.calls.length;
    for (let i = 0; i < 10; i++) {
      noteBackendReachable();
      await Promise.resolve();
    }
    expect(getFileSummariesSettings).toHaveBeenCalledOnce();
    expect(refreshPreflight).toHaveBeenCalledTimes(preflightReads);
  });

  it("refreshes preflight on recovery but not on a planned stream open", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    mod.subscribeProjectEvents(appStore, "proj-1");
    const afterConnect = refreshPreflight.mock.calls.length;

    lastSubscribeOptions?.onOpen?.("initial");
    expect(refreshPreflight).toHaveBeenCalledTimes(afterConnect);

    lastSubscribeOptions?.onOpen?.("resume");
    expect(refreshPreflight).toHaveBeenCalledTimes(afterConnect);

    lastSubscribeOptions?.onOpen?.("reconnect");
    expect(refreshPreflight).toHaveBeenCalledTimes(afterConnect + 1);
  });

  // CriticalStop renders connectivity failures.
  it("keeps connectivity off the rail even for user-initiated connect", async () => {
    discoverBackend.mockRejectedValue(new TypeError("Load failed"));
    const appStore = createAppStore();
    const { mod, all } = await loadModuleWithNotices();
    await expect(
      mod.connectAppBackend(appStore, { reportFailure: true }),
    ).rejects.toThrow("Load failed");
    expect(all()).toHaveLength(0);
  });

  it("keeps connectivity off the rail when user connect races bootstrap", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    discoverBackend.mockImplementation(async () => {
      await gate;
      throw new TypeError("Load failed");
    });
    const appStore = createAppStore();
    const { mod, all } = await loadModuleWithNotices();
    const boot = mod.connectAppBackend(appStore);
    const user = mod.connectAppBackend(appStore, { reportFailure: true });
    release();
    await expect(boot).rejects.toThrow("Load failed");
    await expect(user).rejects.toThrow("Load failed");
    expect(all()).toHaveLength(0);
  });

  it("publishes a structured failure for user-initiated connect", async () => {
    discoverBackend.mockRejectedValue(
      new LycaonApiError("Token rejected.", 401, "unauthorized", {
        title: "Not authorized",
      }),
    );
    const appStore = createAppStore();
    const { mod, all } = await loadModuleWithNotices();
    await expect(
      mod.connectAppBackend(appStore, { reportFailure: true }),
    ).rejects.toThrow("Token rejected.");
    expect(all()).toHaveLength(1);
    expect(all()[0]?.title).toBe("Not authorized");
  });

  it("does not subscribe a successful handshake replaced before its continuation", async () => {
    const mod = await loadModule();
    const appStore = createAppStore();
    let second!: (host: typeof TEST_HOST_INFO) => void;
    getHost.mockResolvedValueOnce(TEST_HOST_INFO).mockImplementationOnce(() => new Promise((resolve) => { second = resolve; }));
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8788", apiToken: "first" });
    queueMicrotask(() => mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8789", apiToken: "second" }));
    await vi.waitFor(() => expect(getHost).toHaveBeenCalledTimes(2));
    expect(subscribeEvents).not.toHaveBeenCalled();
    second(TEST_HOST_INFO);
    await vi.waitFor(() => expect(subscribeEvents).toHaveBeenCalledOnce());
  });

  it("uses no routes of a host serving a different major contract", async () => {
    getHost.mockResolvedValue(testHostInfo({ contract_version: "2.0.0" }));
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    await mod.connectAppBackend(appStore);

    expect(incompatibleHost()?.contract_version).toBe("2.0.0");
    expect(client.listProjects).not.toHaveBeenCalled();
    expect(subscribeEvents).not.toHaveBeenCalled();
    expect(appStore.state.sidecarStatus).toBe("connected");
  });

  it("fails the connection when the host handshake cannot be read", async () => {
    getHost.mockRejectedValue(new LycaonApiError("unavailable", 500, "internal_error"));
    const appStore = createAppStore();
    const client = { listProjects: vi.fn(async () => []) };
    createLycaonClient.mockReturnValue(client);

    const mod = await loadModule();
    await expect(mod.connectAppBackend(appStore)).rejects.toThrow("unavailable");
    expect(client.listProjects).not.toHaveBeenCalled();
    expect(appStore.state.sidecarStatus).toBe("disconnected");
  });

  it("disconnectAppBackend marks the app offline", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    mod.registerAppStore(appStore);
    await mod.connectAppBackend(appStore);
    expect(appStore.state.sidecarStatus).toBe("connected");
    mod.disconnectAppBackend();
    expect(appStore.state.sidecarStatus).toBe("disconnected");
  });

});
