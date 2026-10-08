import { createSettingsStore } from "../../store/settings-store.ts";
import { createCostStore } from "../../store/cost-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { CostSummary } from "../../api/types.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createAttentionStore } from "../../store/attention-store.ts";
import { createEntityRetire } from "../../lifecycle/entity-retire.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { LycaonApiError } from "../../api/http.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import type { EventScope, SourceChangesEvent } from "../../api/types.ts";
import type { EventOpenReason } from "../../api/events.ts";
import {
  connectSourceTreeWorkspace,
  resetSourceTreeStoreForTests,
} from "../../files/tree/source-tree-store.ts";
import {
  noteBackendReachable,
  noteBackendUnreachable,
} from "./request-connectivity.ts";
import { hostIdentity, incompatibleHost } from "./host-identity.ts";
import { TEST_HOST_INFO, testHostInfo } from "./host-identity-test.ts";

const watchWindowExit = vi.hoisted(() => vi.fn((
  _flush: () => void | Promise<void>,
  _resources?: { beforeDestroy: () => void | Promise<void>; onKeepOpen: () => void },
) => vi.fn()));
vi.mock("../windows/watch-window-exit.ts", () => ({ watchWindowExit }));

const {
  reconcileActiveScope,
  reconcileRecentsWithStores,
  refreshCodeScanCache,
  fetchHealth,
  noteStoreRevision,
  noteHealthResponse,
  resetStoreRevisionTracking,
  discoverBackend,
  createLycaonClient,
  subscribeEvents,
  resetSessionEventRevisions,
  refreshPreflight,
  getHost,
} = vi.hoisted(() => ({
  reconcileActiveScope: vi.fn(),
  reconcileRecentsWithStores: vi.fn(),
  refreshCodeScanCache: vi.fn(),
  fetchHealth: vi.fn(),
  noteStoreRevision: vi.fn(),
  noteHealthResponse: vi.fn(() => null),
  resetStoreRevisionTracking: vi.fn(),
  discoverBackend: vi.fn(),
  createLycaonClient: vi.fn(),
  subscribeEvents: vi.fn(),
  resetSessionEventRevisions: vi.fn(),
  refreshPreflight: vi.fn(() => Promise.resolve()),
  getHost: vi.fn(),
}));

/** Every client answers the handshake unless a case stubs its own. */
function withHandshake<T>(client: T): T {
  if (client && typeof client === "object" && !Reflect.has(client, "getHost")) {
    Object.defineProperty(client, "getHost", { value: getHost, configurable: true });
  }
  return client;
}

vi.mock("../persistence/preflight-store.ts", () => ({ refreshPreflight, resetPreflightStore: vi.fn() }));

vi.mock("../../chat/session/session-reconcile.ts", () => ({
  reconcileActiveScope,
  reconcileRecentsWithStores,
  refreshCodeScanCache,
}));

vi.mock("./health.ts", () => ({
  noteStoreRevision,
  noteHealthResponse,
  resetStoreRevisionTracking,
  lastSeenStoreRevision: vi.fn(),
}));

vi.mock("./backend.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./backend.ts")>();
  return {
    ...actual,
    discoverBackend,
    fetchHealth,
    getBackendConnection: () => ({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "tok",
    }),
    clearBackendConnection: vi.fn(),
    readSidecarInfo,
  };
});

const readSidecarInfo = vi.hoisted(() => vi.fn());
let engineStateHandler: ((state: import("./engine-supervision.ts").EngineState) => void) | undefined;
vi.mock("./engine-supervision.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./engine-supervision.ts")>()),
  watchEngineState: vi.fn(async (onChange: (state: import("./engine-supervision.ts").EngineState) => void) => {
    engineStateHandler = onChange;
    return () => {};
  }),
}));

vi.mock("../../api/client-impl.ts", () => ({
  createLycaonClient: (...args: unknown[]) => withHandshake(createLycaonClient(...args)),
}));

vi.mock("../../api/events.ts", () => ({
  subscribeEvents,
  resetSessionEventRevisions,
}));

type SubscribeOptions = {
  onOpen?: (reason: EventOpenReason) => void;
  onInvalidate?: (keys: string[], scope: EventScope) => void;
};

type SubscribeHandlers = {
  session?: (
    event: {
      id: string;
      project_id: string;
      action: "created" | "updated" | "deleted";
      status: "idle" | "busy" | "error";
    },
    scope: EventScope,
  ) => void;
  findings?: (event: { revision: number }, scope: EventScope) => void;
  source_changed?: (event: SourceChangesEvent, scope: EventScope) => void;
};

let lastSubscribeOptions: SubscribeOptions | undefined;
let lastSubscribeHandlers: SubscribeHandlers | undefined;
let subscribedProjectIds: string[] = [];
let closeMock: ReturnType<typeof vi.fn<() => Promise<void>>>;

describe("app-connection cache reconcile", () => {
  let registeredShell: ReturnType<typeof createShellStore>;

  beforeEach(async () => {
    watchWindowExit.mockClear();
    reconcileActiveScope.mockReset();
    reconcileRecentsWithStores.mockReset();
    refreshCodeScanCache.mockReset();
    fetchHealth.mockReset();
    noteStoreRevision.mockReset();
    noteHealthResponse.mockReset();
    noteHealthResponse.mockReturnValue(null);
    resetStoreRevisionTracking.mockReset();
    discoverBackend.mockReset();
    createLycaonClient.mockReset();
    subscribeEvents.mockReset();
    resetSessionEventRevisions.mockReset();
    refreshPreflight.mockClear();
    getHost.mockReset();
    getHost.mockResolvedValue(TEST_HOST_INFO);
    lastSubscribeOptions = undefined;
    lastSubscribeHandlers = undefined;
    subscribedProjectIds = [];
    closeMock = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);

    discoverBackend.mockResolvedValue({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "tok",
    });
    fetchHealth.mockResolvedValue({
      status: "ok",
      version: "0.1.0",
      store_revision: 1,
      schema_version: 1,
    });
    noteStoreRevision.mockReturnValue(false);
    createLycaonClient.mockReturnValue({
      listProjects: vi.fn(async () => []),
      getProject: vi.fn(async (_id: string) => {
        throw new LycaonApiError("missing", 404, "project_not_found");
      }),
    });
    subscribeEvents.mockImplementation((_conn, projectId, handlers, options) => {
      subscribedProjectIds.push(projectId);
      lastSubscribeHandlers = handlers;
      lastSubscribeOptions = options;
      return {
        close: closeMock,
        ready: Promise.resolve(),
        wakeReconnect: vi.fn(),
        url: "http://test/events",
      };
    });
    refreshCodeScanCache.mockResolvedValue(undefined);
    reconcileRecentsWithStores.mockResolvedValue([]);
    reconcileActiveScope.mockResolvedValue(undefined);
    const mod = await import("./app-connection.ts");
    registeredShell = createShellStore("connected");
    mod.registerShellStore(registeredShell);
    mod.registerProjectsStore(createProjectsStore(mod.getLycaonClient, {
      onProjectEvent: () => () => {},
    }));
    mod.registerSettingsStore(createSettingsStore());
    mod.registerCostStore(createCostStore());
    mod.registerRecentsStore(createRecentsStore());
  });

  afterEach(async () => {
    const mod = await import("./app-connection.ts");
    mod.disconnectAppBackend();
    resetSourceTreeStoreForTests();
  });

  async function loadModule() {
    return import("./app-connection.ts");
  }

  /** A gone chat retires through the entity lifecycle, which removes it from recents. */
  function testEntityRetire(recents: ReturnType<typeof createRecentsStore>) {
    return createEntityRetire({
      notices: createNoticeStore(),
      attention: createAttentionStore(() => null, { onAttentionEvent: () => () => {} }),
      recents,
    });
  }

  /** Reads connection notices from their scoped store. */
  async function loadModuleWithNotices() {
    const mod = await import("./app-connection.ts");
    const notices = createNoticeStore();
    mod.registerNoticeStore(notices);
    const all = () => [...notices.index().values()].flat();
    return { mod, all };
  }

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

  it("keeps an attached home window subscribed for engine recovery", async () => {
    const mod = await loadModule();
    const appStore = createAppStore();
    mod.attachKnownBackend(appStore, { baseUrl: "http://127.0.0.1:8788", apiToken: "attached" });
    await vi.waitFor(() => expect(subscribeEvents).toHaveBeenCalledOnce());
    expect(subscribedProjectIds).toEqual([""]);
    lastSubscribeOptions?.onReconnectAttempt?.();
    appStore.actions.setSidecarStatus("disconnected");
    lastSubscribeOptions?.onOpen?.("reconnect");
    expect(appStore.state.sidecarStatus).toBe("connected");
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

  it("skips reconcile on first connect when revision is new", async () => {
    noteStoreRevision.mockReturnValue(false);
    const appStore = createAppStore();
    const mod = await loadModule();
    await mod.connectAppBackend(appStore);
    expect(reconcileRecentsWithStores).not.toHaveBeenCalled();
    expect(reconcileActiveScope).not.toHaveBeenCalled();
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
      });
    });
    expect(reconcileRecentsWithStores).not.toHaveBeenCalled();
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

  it("disconnectAppBackend marks the app offline", async () => {
    const appStore = createAppStore();
    const mod = await loadModule();
    mod.registerAppStore(appStore);
    await mod.connectAppBackend(appStore);
    expect(appStore.state.sidecarStatus).toBe("connected");
    mod.disconnectAppBackend();
    expect(appStore.state.sidecarStatus).toBe("disconnected");
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

  describe("engine supervision", () => {
    const exit = { signal: 9, description: "was killed by signal 9 (SIGKILL)" };

    beforeEach(() => {
      engineStateHandler = undefined;
      readSidecarInfo.mockReset();
    });

    it("rebinds to the replacement engine and withdraws the restarting notice", async () => {
      const { mod, all } = await loadModuleWithNotices();
      const appStore = createAppStore();
      discoverBackend.mockResolvedValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok", engineGeneration: 1 });
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      expect(mod.boundEngineGeneration()).toBe(1);

      engineStateHandler?.({ state: "restarting", exit, attempt: 1 });
      expect(appStore.state.sidecarStatus).toBe("disconnected");
      expect(all().map((notice) => notice.code)).toContain("engine_restarting");

      const replacement = { baseUrl: "http://127.0.0.1:9911", apiToken: "replacement", engineGeneration: 2 };
      readSidecarInfo.mockResolvedValue(replacement);
      createLycaonClient.mockClear();
      engineStateHandler?.({ state: "running", generation: 2 });
      await vi.waitFor(() => expect(mod.boundEngineGeneration()).toBe(2));

      expect(createLycaonClient).toHaveBeenCalledWith(replacement);
      expect(discoverBackend).toHaveBeenCalledTimes(1);
      expect(appStore.state.sidecarStatus).toBe("connected");
      expect(all().map((notice) => notice.code)).not.toContain("engine_restarting");
    });

    it("leaves a booting window to its own connect", async () => {
      const mod = await loadModule();
      await mod.followEngineState(createAppStore());
      engineStateHandler?.({ state: "running", generation: 1 });
      await Promise.resolve();
      expect(readSidecarInfo).not.toHaveBeenCalled();
    });

    it("keeps the engine it is already bound to", async () => {
      const mod = await loadModule();
      const appStore = createAppStore();
      discoverBackend.mockResolvedValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok", engineGeneration: 3 });
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      engineStateHandler?.({ state: "running", generation: 3 });
      await Promise.resolve();
      expect(readSidecarInfo).not.toHaveBeenCalled();
    });

    it("rebinds a window that learns of an intentional restart elsewhere", async () => {
      const mod = await loadModule();
      const appStore = createAppStore();
      discoverBackend.mockResolvedValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok", engineGeneration: 4 });
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      readSidecarInfo.mockResolvedValue({ baseUrl: "http://127.0.0.1:9912", apiToken: "restored", engineGeneration: 5 });
      engineStateHandler?.({ state: "idle" });
      engineStateHandler?.({ state: "running", generation: 5 });
      await vi.waitFor(() => expect(mod.boundEngineGeneration()).toBe(5));
    });

    it("goes offline without a stop of its own when the shell gives up", async () => {
      const mod = await loadModule();
      const appStore = createAppStore();
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      engineStateHandler?.({ state: "stopped", exit });
      expect(appStore.state.sidecarStatus).toBe("disconnected");
      expect(readSidecarInfo).not.toHaveBeenCalled();
    });
  });
});
