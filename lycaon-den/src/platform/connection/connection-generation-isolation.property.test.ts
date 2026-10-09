import { describe, expect, it, vi } from "vitest";
import {
  installConnectionFixtureCleanup,
  createLycaonClient,
  fetchHealth,
  noteStoreRevision,
  subscribeEvents,
  loadModule,
  testEntityRetire,
  reconcileActiveScope,
} from "./app-connection-test-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import { createCostStore } from "../../store/cost-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { LycaonApiError } from "../../api/http.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { TEST_HOST_INFO, testHostInfo } from "./host-identity-test.ts";
import type { HostInfo, Project, Session, SettingsArea, EventScope } from "../../api/types.ts";
import type { TopicHandlers, EventSubscriptionOptions, StoreInvalidation } from "../../api/events.ts";
import { ConnectionHost } from "./connection-host.ts";
import { ConnectionInvalidation } from "./connection-invalidation.ts";

installConnectionFixtureCleanup();

type AppConnectionModule = Awaited<ReturnType<typeof loadModule>>;

type CapturedSubscription = {
  projectId: string;
  handlers: TopicHandlers;
  options: EventSubscriptionOptions;
  close: ReturnType<typeof vi.fn<() => Promise<void>>>;
  isClosed: () => boolean;
};

type TestStores = {
  appStore: ReturnType<typeof createAppStore>;
  shellStore: ReturnType<typeof createShellStore>;
  projectsStore: ReturnType<typeof createProjectsStore>;
  recentsStore: ReturnType<typeof createRecentsStore>;
  settingsStore: ReturnType<typeof createSettingsStore>;
  costStore: ReturnType<typeof createCostStore>;
  noticeStore: ReturnType<typeof createNoticeStore>;
};

function setupStores(mod: AppConnectionModule): TestStores {
  const appStore = createAppStore();
  const shellStore = createShellStore("connected");
  const recentsStore = createRecentsStore();
  const noticeStore = createNoticeStore();
  const retire = testEntityRetire(recentsStore);
  const projectsStore = createProjectsStore(mod.getLycaonClient, { onProjectEvent: () => () => {} });
  const settingsStore = createSettingsStore();
  const costStore = createCostStore();

  mod.registerAppStore(appStore);
  mod.registerShellStore(shellStore);
  mod.registerProjectsStore(projectsStore);
  mod.registerSettingsStore(settingsStore);
  mod.registerCostStore(costStore);
  mod.registerRecentsStore(recentsStore);
  mod.registerNoticeStore(noticeStore);
  mod.registerEntityRetire(retire);

  return { appStore, shellStore, projectsStore, recentsStore, settingsStore, costStore, noticeStore };
}

function snapshotPurityState(stores: TestStores): string {
  const raw = {
    app: {
      sidecarStatus: stores.appStore.state.sidecarStatus,
      isLoading: stores.appStore.state.isLoading,
      currentSession: stores.appStore.state.currentSession,
      approvalsRevision: stores.appStore.state.approvalsRevision,
      extensionsRevision: stores.appStore.state.extensionsRevision,
      modelPolicyRevision: stores.appStore.state.modelPolicyRevision,
      projectTrustRevision: stores.appStore.state.projectTrustRevision,
      verifyDetectRevision: stores.appStore.state.verifyDetectRevision,
      findings: stores.appStore.state.findings,
      progress: stores.appStore.state.progress,
      queueDraft: stores.appStore.state.queueDraft,
    },
    projects: stores.projectsStore.state.projects.map((p) => ({ id: p.id, name: p.name })),
    recents: stores.recentsStore.state.recents.map((r) => ({ sessionId: r.sessionId, projectId: r.projectId })),
    settings: {
      providers: stores.settingsStore.state.providers,
      providerKinds: stores.settingsStore.state.providerKinds,
      modelPolicy: stores.settingsStore.state.modelPolicy,
      pricing: stores.settingsStore.state.pricing,
    },
    shell: {
      activeProjectId: stores.shellStore.state.activeProjectId,
      foreground: stores.shellStore.state.foreground,
    },
    costs: {
      session: stores.costStore.state.session,
      loading: stores.costStore.state.loading,
    },
    notices: [...stores.noticeStore.index().values()].flat().map((n) => n.code).sort(),
  };
  return JSON.stringify(raw);
}

function createSessionFixture(id: string, projectId: string): Session {
  return {
    id,
    project_id: projectId,
    workspace_path: `/tmp/${projectId}`,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    posture: "build",
    status: "idle",
    created_at: "2026-10-09T00:00:00Z",
    activity_at: "2026-10-09T00:00:00Z",
    updated_at: "2026-10-09T00:00:00Z",
  };
}

describe("connection generation isolation - domain level invariants", () => {
  it("ConnectionHost enforces strict generation and client fencing on assertCurrent", () => {
    createLycaonClient.mockImplementation(() => stubClient({}));
    const host = new ConnectionHost();
    const conn1 = { baseUrl: "http://127.0.0.1:8788", apiToken: "tok-1" };
    const conn2 = { baseUrl: "http://127.0.0.1:8789", apiToken: "tok-2" };

    const client1 = host.bind(conn1);
    const gen0 = host.generation;
    expect(() => host.assertCurrent(gen0, client1)).not.toThrow();

    host.advance();
    const gen1 = host.generation;
    expect(gen1).toBe(gen0 + 1);

    expect(() => host.assertCurrent(gen0, client1)).toThrowError(
      expect.objectContaining({ name: "AbortError", message: "Backend connection changed." }),
    );

    const client2 = host.bind(conn2);
    expect(() => host.assertCurrent(gen1, client1)).toThrowError(
      expect.objectContaining({ name: "AbortError" }),
    );
    expect(() => host.assertCurrent(gen1, client2)).not.toThrow();

    host.clear();
    expect(() => host.assertCurrent(gen1, client2)).toThrowError(
      expect.objectContaining({ name: "AbortError" }),
    );
  });

  it("ConnectionHost.handshake discards delayed response from prior generation with AbortError", async () => {
    const host = new ConnectionHost();
    let resolveHostInfo!: (info: HostInfo) => void;
    const client0 = stubClient({
      getHost: vi.fn(() => new Promise<HostInfo>((resolve) => { resolveHostInfo = resolve; })),
    });

    const gen0 = host.generation;
    const handshakePromise = host.handshake(client0, gen0);

    host.advance();
    host.bind({ baseUrl: "http://127.0.0.1:8789", apiToken: "tok-1" });

    resolveHostInfo(TEST_HOST_INFO);
    await expect(handshakePromise).rejects.toMatchObject({ name: "AbortError" });
  });

  it("ConnectionInvalidation drops settings and store invalidations across bind and cancel", () => {
    let activeClient: ReturnType<typeof stubClient> | null = stubClient();
    const ports = {
      client: () => activeClient,
      projects: () => [],
      settings: () => null,
      costs: () => null,
    };
    const invalidation = new ConnectionInvalidation(ports);
    const appStore = createAppStore();

    invalidation.bind(appStore);
    invalidation.cancel();
    activeClient = null;

    expect(() => {
      invalidation.settingsEvent(appStore, { area: "approvals" as SettingsArea, scope: "project", action: "update" });
      invalidation.invalidate(appStore, ["providers" as StoreInvalidation], { kind: "device" });
    }).not.toThrow();

    expect(appStore.state.approvalsRevision).toBe(0);
    expect(appStore.state.modelPolicyRevision).toBe(0);
  });
});

describe("connection generation isolation - system level generation fencing", () => {
  it("Generation Fencing Invariant: delayed Gen N handshake response arriving after Gen N+1 has zero effect", async () => {
    const mod = await loadModule();
    const stores = setupStores(mod);

    let resolveHandshake0!: (info: HostInfo) => void;
    const client0 = stubClient({
      listProjects: async () => [],
      getHost: vi.fn(() => new Promise<HostInfo>((resolve) => { resolveHandshake0 = resolve; })),
    });
    createLycaonClient.mockReturnValueOnce(client0);

    mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9000", apiToken: "tok-gen0" });
    expect(client0.getHost).toHaveBeenCalledOnce();

    const client1 = stubClient({ listProjects: async () => [wireProject("/p1", "proj-1")] });
    createLycaonClient.mockReturnValueOnce(client1);

    mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9001", apiToken: "tok-gen1" });
    expect(stores.appStore.state.sidecarStatus).toBe("connected");
    expect(mod.getLycaonClient()).toBe(client1);

    noteStoreRevision.mockClear();

    resolveHandshake0(testHostInfo({ product_version: "incompatible-version" }));
    await new Promise<void>((resolve) => setImmediate(resolve));

    expect(noteStoreRevision).not.toHaveBeenCalled();
    expect(stores.appStore.state.sidecarStatus).toBe("connected");
    expect(stores.noticeStore.index().size).toBe(0);
  });

  it("Generation Fencing Invariant: delayed Gen N health check and revision bumps arriving after Gen N+1 have zero effect", async () => {
    const mod = await loadModule();
    const stores = setupStores(mod);

    let resolveHealth0!: (val: { status: string; version: string; store_revision: number; schema_version: number }) => void;
    fetchHealth.mockImplementationOnce(() => new Promise((resolve) => { resolveHealth0 = resolve; }));

    const client0 = stubClient({ listProjects: async () => [] });
    createLycaonClient.mockReturnValueOnce(client0);

    const connectingGen0 = mod.connectAppBackend(stores.appStore);
    await vi.waitFor(() => expect(fetchHealth).toHaveBeenCalledOnce());

    const client1 = stubClient({ listProjects: async () => [wireProject("/p1", "proj-1")] });
    createLycaonClient.mockReturnValueOnce(client1);

    mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9001", apiToken: "tok-gen1" });
    expect(stores.appStore.state.sidecarStatus).toBe("connected");

    noteStoreRevision.mockClear();
    resolveHealth0({ status: "recovery", version: "0.1.0", store_revision: 9999, schema_version: 1 });
    await expect(connectingGen0).rejects.toMatchObject({ name: "AbortError" });
    await new Promise<void>((resolve) => setImmediate(resolve));

    expect(noteStoreRevision).not.toHaveBeenCalled();
    expect(stores.appStore.state.sidecarStatus).toBe("connected");
  });

  it("Generation Fencing Invariant: delayed Gen N project queries and 404s never evict Gen N+1 active shell scope", async () => {
    const mod = await loadModule();
    const stores = setupStores(mod);

    stores.shellStore.commitStageScope({ projectId: "proj-gen0", sessionId: "sess-gen0" });
    let rejectGetProject0!: (err: Error) => void;

    const client0 = stubClient({
      listProjects: async () => [],
      getProject: vi.fn(() => new Promise<Project>((_resolve, reject) => {
        rejectGetProject0 = reject;
      })),
    });
    createLycaonClient.mockReturnValueOnce(client0);

    const connectingGen0 = mod.connectAppBackend(stores.appStore);
    await vi.waitFor(() => expect(client0.getProject).toHaveBeenCalledOnce());

    const project1 = wireProject("/p1", "proj-gen1");
    const client1 = stubClient({
      listProjects: async () => [project1],
      getProject: vi.fn(async () => project1),
    });
    createLycaonClient.mockReturnValueOnce(client1);

    stores.shellStore.commitStageScope({ projectId: "proj-gen1", sessionId: "sess-gen1" });
    mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9002", apiToken: "tok-gen1" });
    stores.projectsStore.upsert(project1);

    rejectGetProject0(new LycaonApiError("Missing from gen0", 404, "project_not_found"));
    await expect(connectingGen0).rejects.toMatchObject({ name: "AbortError" });
    await new Promise<void>((resolve) => setImmediate(resolve));

    expect(stores.shellStore.state.activeProjectId).toBe("proj-gen1");
    expect(stores.shellStore.state.foreground).toEqual({ projectId: "proj-gen1", sessionId: "sess-gen1" });
    expect(stores.projectsStore.byId("proj-gen1")).toBeDefined();
    expect(stores.projectsStore.byId("proj-gen0")).toBeUndefined();
  });

  it("Generation Fencing Invariant: delayed Gen N active scope reconcile and session 404 never mutates Gen N+1 session", async () => {
    const mod = await loadModule();
    const stores = setupStores(mod);

    const actual = await vi.importActual<typeof import("../../chat/session/session-reconcile.ts")>(
      "../../chat/session/session-reconcile.ts",
    );
    reconcileActiveScope.mockImplementation(actual.reconcileActiveScope);

    let capturedOptions: EventSubscriptionOptions | undefined;
    subscribeEvents.mockImplementation((_conn, _pid, _h, options) => {
      capturedOptions = options;
      return {
        close: vi.fn().mockResolvedValue(undefined),
        ready: Promise.resolve(),
        wakeReconnect: vi.fn(),
        url: "http://test/events",
      };
    });

    let rejectBootstrap0!: (err: Error) => void;
    const client0 = stubClient({
      listProjects: async () => [],
      getSessionBootstrap: vi.fn(() => new Promise((_resolve, reject) => { rejectBootstrap0 = reject; })),
    });
    createLycaonClient.mockReturnValueOnce(client0);

    await mod.connectAppBackend(stores.appStore);
    stores.appStore.actions.setCurrentSession(createSessionFixture("session-gen0", "proj-gen0"));

    // Trigger reconnect on Gen 0 stream to begin in-flight scope reconciliation
    capturedOptions?.onOpen?.("reconnect");
    await vi.waitFor(() => expect(client0.getSessionBootstrap).toHaveBeenCalledOnce());

    const client1 = stubClient({ listProjects: async () => [] });
    createLycaonClient.mockReturnValueOnce(client1);

    mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9003", apiToken: "tok-gen1" });
    stores.appStore.actions.setCurrentSession(createSessionFixture("session-gen1", "proj-gen1"));

    const resetSpy = vi.spyOn(stores.appStore.actions, "resetChatForSessionSwitch");
    rejectBootstrap0(new LycaonApiError("Gen 0 session gone", 404, "session_not_found"));
    await new Promise<void>((resolve) => setImmediate(resolve));

    expect(resetSpy).not.toHaveBeenCalled();
    expect(stores.appStore.state.currentSession?.id).toBe("session-gen1");
    expect(stores.appStore.state.currentSession?.project_id).toBe("proj-gen1");
  });

  it("Generation Fencing Invariant: events delivered to Gen N SSE subscription have ZERO effect on Gen N+1", async () => {
    const mod = await loadModule();
    const stores = setupStores(mod);

    const captured: CapturedSubscription[] = [];
    subscribeEvents.mockImplementation((_conn, projectId, handlers, options) => {
      let closed = false;
      const sub: CapturedSubscription = {
        projectId,
        handlers: handlers as TopicHandlers,
        options: options as EventSubscriptionOptions,
        close: vi.fn(async () => { closed = true; }),
        isClosed: () => closed,
      };
      captured.push(sub);
      return {
        close: sub.close,
        ready: Promise.resolve(),
        wakeReconnect: vi.fn(),
        url: "http://test/events",
      };
    });

    const client0 = stubClient({ listProjects: async () => [] });
    createLycaonClient.mockReturnValueOnce(client0);
    await mod.connectAppBackend(stores.appStore);
    mod.subscribeProjectEvents(stores.appStore, "proj-gen0");
    expect(captured).toHaveLength(2); // Device-wide + project
    const gen0Sub = captured[1];
    expect(gen0Sub).toBeDefined();

    const client1 = stubClient({ listProjects: async () => [] });
    createLycaonClient.mockReturnValueOnce(client1);
    mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9004", apiToken: "tok-gen1" });
    mod.subscribeProjectEvents(stores.appStore, "proj-gen1");
    expect(gen0Sub!.close).toHaveBeenCalled();
    expect(gen0Sub!.isClosed()).toBe(true);

    stores.appStore.actions.setCurrentSession(createSessionFixture("session-gen1", "proj-gen1"));
    const baselineSnapshot = snapshotPurityState(stores);

    const gen0Scope: EventScope = { kind: "session", project_id: "proj-gen0", session_id: "session-gen0" };

    // Fenced topic events arriving for Gen 0
    gen0Sub!.handlers.session?.({
      id: "session-gen0",
      project_id: "proj-gen0",
      action: "deleted",
      status: "idle",
    }, gen0Scope);

    gen0Sub!.handlers.findings?.({ revision: 999 }, gen0Scope);
    gen0Sub!.handlers.progress?.({ revision: 999 }, gen0Scope);
    gen0Sub!.handlers.queue?.({ revision: 999 }, gen0Scope);

    await new Promise<void>((resolve) => setImmediate(resolve));

    const afterInterferenceSnapshot = snapshotPurityState(stores);
    expect(afterInterferenceSnapshot).toEqual(baselineSnapshot);
  });
});

describe("connection generation isolation - state purity property", () => {
  it("State Purity Invariant: generation N+1 state remains bitwise identical to an unperturbed clean bootstrap despite full adversarial Gen N race", async () => {
    const mod = await loadModule();

    // --- Scenario A: Perturbed by Generation 0 ---
    const perturbedStores = setupStores(mod);
    let resolveGen0Handshake!: (info: HostInfo) => void;

    const client0 = stubClient({
      listProjects: async () => [wireProject("/p0", "proj-gen0")],
      getHost: vi.fn(() => new Promise<HostInfo>((resolve) => { resolveGen0Handshake = resolve; })),
    });
    createLycaonClient.mockReturnValueOnce(client0);

    mod.attachKnownBackend(perturbedStores.appStore, { baseUrl: "http://127.0.0.1:9010", apiToken: "tok-gen0" });
    expect(client0.getHost).toHaveBeenCalledOnce();

    const project1 = wireProject("/p1", "proj-clean");
    const client1 = stubClient({
      listProjects: async () => [project1],
      getProject: async () => project1,
      getHost: async () => TEST_HOST_INFO,
    });
    createLycaonClient.mockReturnValueOnce(client1);

    mod.attachKnownBackend(perturbedStores.appStore, { baseUrl: "http://127.0.0.1:9011", apiToken: "tok-clean" });
    perturbedStores.shellStore.commitStageScope({ projectId: "proj-clean", sessionId: "sess-clean" });
    perturbedStores.appStore.actions.setCurrentSession(createSessionFixture("sess-clean", "proj-clean"));
    mod.subscribeProjectEvents(perturbedStores.appStore, "proj-clean");

    // Resolve delayed Gen 0 handshake after Gen 1 is established
    resolveGen0Handshake(testHostInfo({ product_version: "invalid" }));
    await new Promise<void>((resolve) => setImmediate(resolve));

    const perturbedStateJson = snapshotPurityState(perturbedStores);

    // --- Scenario B: Unperturbed Clean Generation 1 Bootstrap ---
    mod.disconnectAppBackend();
    const cleanStores = setupStores(mod);

    const clientClean = stubClient({
      listProjects: async () => [project1],
      getProject: async () => project1,
      getHost: async () => TEST_HOST_INFO,
    });
    createLycaonClient.mockReturnValueOnce(clientClean);

    mod.attachKnownBackend(cleanStores.appStore, { baseUrl: "http://127.0.0.1:9011", apiToken: "tok-clean" });
    cleanStores.shellStore.commitStageScope({ projectId: "proj-clean", sessionId: "sess-clean" });
    cleanStores.appStore.actions.setCurrentSession(createSessionFixture("sess-clean", "proj-clean"));
    mod.subscribeProjectEvents(cleanStores.appStore, "proj-clean");
    await new Promise<void>((resolve) => setImmediate(resolve));

    const cleanStateJson = snapshotPurityState(cleanStores);

    expect(perturbedStateJson).toEqual(cleanStateJson);
  });

  it("Property: deterministic pseudo-random interleaving across 3 successive generations preserves state purity and generation fencing", async () => {
    const mod = await loadModule();

    let seed = 0xdeadbeef;
    function rand(): number {
      seed = (seed * 1664525 + 1013904223) >>> 0;
      return seed / 4294967296;
    }

    for (let iteration = 0; iteration < 3; iteration++) {
      mod.disconnectAppBackend();
      const stores = setupStores(mod);

      const capturedSubs: CapturedSubscription[] = [];
      subscribeEvents.mockImplementation((_conn, projectId, handlers, options) => {
        let closed = false;
        const sub: CapturedSubscription = {
          projectId,
          handlers: handlers as TopicHandlers,
          options: options as EventSubscriptionOptions,
          close: vi.fn(async () => { closed = true; }),
          isClosed: () => closed,
        };
        capturedSubs.push(sub);
        return {
          close: sub.close,
          ready: Promise.resolve(),
          wakeReconnect: vi.fn(),
          url: "http://test/events",
        };
      });

      const delayedResolvers: Array<() => void> = [];

      // Step 1: Gen 0 attach
      const client0 = stubClient({
        listProjects: async () => [wireProject("/p0", "proj-0")],
        getHost: () => new Promise<HostInfo>((resolve) => {
          delayedResolvers.push(() => resolve(testHostInfo({ product_version: "gen0" })));
        }),
      });
      createLycaonClient.mockReturnValueOnce(client0);
      mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9100", apiToken: "tok-0" });
      mod.subscribeProjectEvents(stores.appStore, "proj-0");

      // Step 2: Gen 1 attach
      const client1 = stubClient({
        listProjects: async () => [wireProject("/p1", "proj-1")],
        getHost: () => new Promise<HostInfo>((resolve) => {
          delayedResolvers.push(() => resolve(testHostInfo({ product_version: "gen1" })));
        }),
      });
      createLycaonClient.mockReturnValueOnce(client1);
      mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9101", apiToken: "tok-1" });
      mod.subscribeProjectEvents(stores.appStore, "proj-1");

      // Step 3: Gen 2 attach (final stable generation)
      const project2 = wireProject("/p2", "proj-2");
      const client2 = stubClient({
        listProjects: async () => [project2],
        getProject: async () => project2,
        getHost: async () => TEST_HOST_INFO,
      });
      createLycaonClient.mockReturnValueOnce(client2);
      mod.attachKnownBackend(stores.appStore, { baseUrl: "http://127.0.0.1:9102", apiToken: "tok-2" });
      stores.shellStore.commitStageScope({ projectId: "proj-2", sessionId: "sess-2" });
      stores.appStore.actions.setCurrentSession(createSessionFixture("sess-2", "proj-2"));
      mod.subscribeProjectEvents(stores.appStore, "proj-2");

      // Step 4: Flush delayed Gen 0 and Gen 1 actions in pseudo-random order
      if (rand() > 0.5) delayedResolvers.reverse();
      for (const resolver of delayedResolvers) resolver();

      // Step 5: Deliver cross-generation adversarial SSE events (only to closed streams)
      const staleSubs = capturedSubs.filter((s) => s.isClosed());
      for (const stale of staleSubs) {
        const scope: EventScope = { kind: "session", project_id: stale.projectId, session_id: "stale" };
        stale.handlers.findings?.({ revision: 50 }, scope);
        stale.handlers.progress?.({ revision: 50 }, scope);
      }

      await new Promise<void>((resolve) => setImmediate(resolve));
      const perturbedState = snapshotPurityState(stores);

      // Compare against clean reference run for Gen 2
      mod.disconnectAppBackend();
      const cleanStores = setupStores(mod);
      createLycaonClient.mockReturnValueOnce(client2);
      mod.attachKnownBackend(cleanStores.appStore, { baseUrl: "http://127.0.0.1:9102", apiToken: "tok-2" });
      cleanStores.shellStore.commitStageScope({ projectId: "proj-2", sessionId: "sess-2" });
      cleanStores.appStore.actions.setCurrentSession(createSessionFixture("sess-2", "proj-2"));
      mod.subscribeProjectEvents(cleanStores.appStore, "proj-2");
      await new Promise<void>((resolve) => setImmediate(resolve));

      const cleanState = snapshotPurityState(cleanStores);
      expect(perturbedState).toEqual(cleanState);
    }
  });
});
