import { createSettingsStore } from "../../store/settings-store.ts";
import { createCostStore } from "../../store/cost-store.ts";

import { afterEach, beforeEach, vi } from "vitest";

import { createNoticeStore } from "../../notices/notice-store.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createAttentionStore } from "../../store/attention-store.ts";
import { createEntityRetire } from "../../lifecycle/entity-retire.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { LycaonApiError } from "../../api/http.ts";

import type { EventScope, SourceChangesEvent } from "../../api/types.ts";
import type { EventOpenReason } from "../../api/events.ts";
import { resetSourceTreeStoreForTests } from "../../files/tree/source-tree-store.ts";

import { TEST_HOST_INFO } from "./host-identity-test.ts";

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
  onReconnectAttempt?: (attempt: number, delayMs: number) => void;
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

export let lastSubscribeOptions: SubscribeOptions | undefined;
export let lastSubscribeHandlers: SubscribeHandlers | undefined;
export let subscribedProjectIds: string[] = [];
export let closeMock: ReturnType<typeof vi.fn<() => Promise<void>>>;

export let registeredShell: ReturnType<typeof createShellStore>;

export function installConnectionFixtureCleanup() {
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

}

export async function loadModule() {
    return import("./app-connection.ts");
  }

  /** A gone chat retires through the entity lifecycle, which removes it from recents. */
export function testEntityRetire(recents: ReturnType<typeof createRecentsStore>) {
    return createEntityRetire({
      notices: createNoticeStore(),
      attention: createAttentionStore(() => null, { onAttentionEvent: () => () => {} }),
      recents,
    });
  }

  /** Reads connection notices from their scoped store. */
export async function loadModuleWithNotices() {
    const mod = await import("./app-connection.ts");
    const notices = createNoticeStore();
    mod.registerNoticeStore(notices);
    const all = () => [...notices.index().values()].flat();
    return { mod, all };
  }

export function resetEngineStateHandler(): void { engineStateHandler = undefined; }
export function emitEngineState(state: import("./engine-supervision.ts").EngineState): void { engineStateHandler?.(state); }

export { watchWindowExit, reconcileActiveScope, reconcileRecentsWithStores, refreshCodeScanCache, fetchHealth, noteStoreRevision, noteHealthResponse, resetStoreRevisionTracking, discoverBackend, createLycaonClient, subscribeEvents, resetSessionEventRevisions, refreshPreflight, getHost, readSidecarInfo };
