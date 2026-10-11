import type { ConnectionServices } from "./connection-services.ts";
import type { LycaonClient } from "../../api/client.ts";
import { resetSessionEventRevisions } from "../../api/events.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { NoticeReporter, NoticeStore } from "../../notices/notice-store.ts";
import { registerNoticePublisher } from "../../notices/notice-store.ts";
import type { EntityRetire } from "../../lifecycle/entity-retire.ts";
import { refreshPreflight, resetPreflightStore } from "../persistence/preflight-store.ts";
import { invalidateContributionFrame } from "../../contributions/contribution-store.ts";
import type { NoticeScope } from "../../notices/notice-scope.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { fileSummariesSettingKnown, refreshFileSummariesSetting, resetFileSummariesSetting } from "../../settings/editor/file-summary-settings.ts";
import type { CostStore } from "../../store/cost-store.ts";
import { cancelWorkerTranscriptCoalesce } from "../../chat/worker/worker-transcript-coalesce.ts";
import { clearChildMessageBuffer } from "../../chat/transcript/projection/worker-child-message-buffer.ts";
import { reconcileActiveScope } from "../../chat/session/session-reconcile.ts";
import type { BackendConnection } from "./backend.ts";
import { clearBackendConnection, discoverBackend, fetchHealth, getBackendConnection,  restartBackend, setBackendConnection } from "./backend.ts";
import { createEngineFollower } from "./connection-engine-follower.ts";
import { noteHealthResponse, noteStoreRevision, resetStoreRevisionTracking } from "./health.ts";
import { incompatibleHost } from "./host-identity.ts";
import { setBackendReachabilityObserver } from "./request-connectivity.ts";
import { ConnectionHost } from "./connection-host.ts";
import { createConnectionReconcile } from "./connection-reconcile.ts";
import { ConnectionInvalidation } from "./connection-invalidation.ts";
import { createConnectionEvents } from "./connection-events.ts";

const host = new ConnectionHost();
let services: ConnectionServices | null = null;
let engineFollower: ReturnType<typeof createEngineFollower> | undefined;
let connectInFlight: Promise<LycaonClient> | null = null;
/** OR-merged across concurrent connect callers for this in-flight attempt. */
let connectFailureNotices = false;

const invalidation = new ConnectionInvalidation({
  client: host.getClient,
  projects: () => projectsRegistry(),
  settings: () => services?.settings ?? null,
  costs: () => services?.costs ?? null,
});
const reconcile = createConnectionReconcile(host, {
  shell: () => services?.shell ?? null,
  projects: () => services?.projects ?? null,
  recents: () => services?.recents ?? null,
  retire: () => services?.retire ?? null,
  notices: appNoticeReporter,
  resetEvents: resetBackendEventStream,
  resubscribeForeground: resubscribeForegroundProjectEvents,
});
const events = createConnectionEvents({
  host, invalidation, reconcile,
  projects: () => services?.projects ?? null,
  recents: () => services?.recents ?? null,
  retire: () => services?.retire ?? null,
});
export const projectsRegistry = reconcile.projectsRegistry;
export const handleSessionGone = reconcile.handleSessionGone;
export const getLycaonClient = host.getClient;
export const setLycaonClientForTest = host.setClientForTest;
export const resumeProjectEventsAfter = events.resumeProjectEventsAfter;
export const onProjectEvent = events.onProjectEvent;
export const onAttentionEvent = events.onAttentionEvent;
export const onCLIOpenEvent = events.onCLIOpenEvent;
export const onAttentionResync = events.onAttentionResync;
export const onSessionEvent = events.onSessionEvent;
export const projectEventsAreSubscribed = events.projectEventsAreSubscribed;
export const waitForProjectEvents = events.waitForProjectEvents;
export const subscribeProjectEvents = events.subscribeProjectEvents;

/** Rebinding retires asynchronous work before exposing replacement services. */
export function bindAppConnection(next: ConnectionServices): () => void {
  engineFollower?.dispose();
  engineFollower = undefined;
  disconnectAppBackend();
  const binding = services = Object.freeze({ ...next });
  invalidation.resetSettings();
  registerNoticePublisher(binding.notices);
  setBackendReachabilityObserver({
    reachable: () => { if (services === binding) markSidecarConnected(binding.app); },
    unreachable: () => { if (services === binding) binding.app.actions.setSidecarStatus("disconnected"); },
  });
  return () => {
    if (services !== binding) return;
    engineFollower?.dispose();
    engineFollower = undefined;
    disconnectAppBackend();
    setBackendReachabilityObserver(null);
    registerNoticePublisher(null);
    services = null;
  };
}

/** Cost store for stage views mounted outside the store-prop tree. */
export function getCostStore(): CostStore | null {
  return services?.costs ?? null;
}

/** Refresh readiness when the sidecar becomes reachable. */
function markSidecarConnected(appStore: AppStore): void {
  const previous = appStore.state.sidecarStatus;
  appStore.actions.setSidecarStatus("connected");
  if (previous === "connected") return;
  void refreshPreflight();
  if (host.client && !fileSummariesSettingKnown()) {
    void refreshFileSummariesSetting(host.client).catch(() => undefined);
  }
}

export function getEntityRetire(): EntityRetire | null {
  return services?.retire ?? null;
}

/** Notice store for code outside a reactive context. */
export function getRegisteredNoticeStore(): NoticeStore | null {
  return services?.notices ?? null;
}

/** Reports app-scoped connection and boot failures. */
export function appNoticeReporter(): NoticeReporter {
  return noticeReporterFor(APP_SCOPE);
}

/** Returns a reporter bound to one notice scope. */
export function noticeReporterFor(scope: NoticeScope): NoticeReporter {
  const store = services?.notices ?? null;
  if (!store) return { reportError: () => {}, publish: () => {} };
  return store.reporterFor(scope);
}

/** Resubscribe the foreground project after reconnect. */
function resubscribeForegroundProjectEvents(appStore = services?.app ?? null): void {
  const projectId = services?.shell.state.foreground?.projectId?.trim() ?? "";
  if (!appStore) return;
  subscribeProjectEvents(appStore, projectId);
}

function resetBackendEventStream(appStore: AppStore | null): void {
  events.reset();
  services?.costs.actions.clear();
  resetPreflightStore();
  resetFileSummariesSetting();
  if (appStore) {
    appStore.actions.invalidateSessionViewRequests();
    resetSessionEventRevisions(appStore.actions);
  }
}

/** Attaches a known backend without managing its lifecycle. */
export function attachKnownBackend(
  appStore: AppStore,
  connection: BackendConnection,
): LycaonClient {
  host.advance();
  connectInFlight = null;
  connectFailureNotices = false;
  resetBackendEventStream(appStore);
  setBackendConnection(connection);
  const client = host.bind(connection);
  void invalidateContributionFrame();
  markSidecarConnected(appStore);
  const generation = host.generation;
  void host.handshake(client, generation).then((hostInfo) => {
    host.assertCurrent(generation, client);
    if (hostInfo.compatible) resubscribeForegroundProjectEvents(appStore);
  }).catch(() => undefined);
  return client;
}

export type ConnectAppBackendOpts = {
  /** Reports connection errors for explicit retries. */
  reportFailure?: boolean;
  /** A connection the shell already established, bound instead of discovering one. */
  connection?: BackendConnection;
};

async function verifyCachedBackend(appStore: AppStore): Promise<boolean> {
  const client = host.client;
  if (!client) return false;
  try {
    const generation = host.generation;
    await reconcile.syncProjectsFromBackend(appStore, client);
    host.assertCurrent(generation, client);
    markSidecarConnected(appStore);
    return true;
  } catch {
    return false;
  }
}

/** Connect if needed and hydrate projects. */
export async function connectAppBackend(
  appStore: AppStore,
  opts?: ConnectAppBackendOpts,
): Promise<LycaonClient> {
  if (connectInFlight) {
    if (opts?.reportFailure) connectFailureNotices = true;
    return connectInFlight;
  }
  connectFailureNotices = opts?.reportFailure ?? false;
  const startedGeneration = host.generation;
  const flight = Promise.resolve().then(async () => {
    host.assertCurrent(startedGeneration);
    const known = opts?.connection;
    if (known && host.client) {
      retireBackendConnection();
    } else if (host.client) {
      const client = host.client;
      const generation = host.generation;
      const valid = await verifyCachedBackend(appStore);
      host.assertCurrent(generation, client);
      if (valid) return client;
      retireBackendConnection();
    }
    const discover = known
      ? async () => {
        setBackendConnection(known);
        return known;
      }
      : discoverBackend;
    return connectAppBackendInner(appStore, host.generation, discover);
  }).catch((err) => {
    if (connectInFlight === flight && connectFailureNotices) {
      appNoticeReporter().reportError(
        err instanceof Error ? err : new Error("Backend connection failed"),
      );
    }
    throw err;
  }).finally(() => {
    if (connectInFlight !== flight) return;
    connectInFlight = null;
    connectFailureNotices = false;
  });
  connectInFlight = flight;
  return flight;
}

/** Restart the managed sidecar and fully reconnect this window to the new store. */
export async function restartAppBackend(): Promise<LycaonClient> {
  const appStore = services?.app ?? null;
  if (!appStore) throw new Error("app connection is not initialized");
  disconnectAppBackend();
  const generation = host.generation;
  await restartBackend();
  host.assertCurrent(generation);
  return connectAppBackend(appStore, { reportFailure: true });
}

async function connectAppBackendInner(
  appStore: AppStore,
  generation: number,
  discover: () => Promise<BackendConnection>,
): Promise<LycaonClient> {
  appStore.actions.setSidecarStatus("connecting");
  try {
    const connection = await discover();
    host.assertCurrent(generation);
    resetBackendEventStream(appStore);
    const client = host.bind(connection);
    // Client discovery may finish after hydration begins.
    void invalidateContributionFrame();
    appStore.actions.setLoading(true);

    // Health selects recovery before project routes are called.
    try {
      const health = await fetchHealth(connection.baseUrl);
      host.assertCurrent(generation, client);
      const skew = noteHealthResponse(health);
      if (skew) appNoticeReporter().publish(skew);
      if (health.status === "recovery") {
        noteStoreRevision(health.store_revision);
        markSidecarConnected(appStore);
        return client;
      }
    } catch {
      host.assertCurrent(generation, client);
      /* Project synchronization performs the health probe. */
    }

    await reconcile.syncProjectsFromBackend(appStore, client);
    host.assertCurrent(generation, client);
    if (incompatibleHost()) {
      appStore.actions.setSidecarStatus("connected");
      return client;
    }
    markSidecarConnected(appStore);
    resubscribeForegroundProjectEvents(appStore);
    if (appStore.state.currentSession?.id) {
      await reconcileActiveScope(appStore, client, projectsRegistry(), {
        onSessionGone: handleSessionGone,
        resumeVisible: true,
        shouldApply: () => generation === host.generation && host.client === client,
      });
      host.assertCurrent(generation, client);
    }

    await invalidation.hydrate(client, () => generation === host.generation && host.client === client);
    host.assertCurrent(generation, client);
    return client;
  } catch (err) {
    if (generation !== host.generation) throw err;
    host.clear();
    clearBackendConnection();
    appStore.actions.setSidecarStatus("disconnected");
    throw err;
  } finally {
    if (generation === host.generation) appStore.actions.setLoading(false);
  }
}

/** The shell's engine generation this window's connection belongs to, if it launched one. */
export function boundEngineGeneration(): number | undefined {
  return (host.connection ?? getBackendConnection())?.engineGeneration;
}

/** Follows engine replacements for the active service binding. */
export async function followEngineState(appStore: AppStore): Promise<() => void> {
  engineFollower?.dispose();
  const follower = engineFollower = createEngineFollower({
    app: appStore,
    notices: services?.notices ?? null,
    connecting: () => connectInFlight !== null,
    boundGeneration: boundEngineGeneration,
    connect: (connection) => connectAppBackend(appStore, { connection }),
  });
  await follower.ready;
  return () => {
    follower.dispose();
    if (engineFollower === follower) engineFollower = undefined;
  };
}

export function disconnectAppBackend(): void {
  connectInFlight = null;
  connectFailureNotices = false;
  retireBackendConnection();
}

function retireBackendConnection(): void {
  host.advance();
  resetBackendEventStream(services?.app ?? null);
  events.dispose();
  cancelWorkerTranscriptCoalesce();
  clearChildMessageBuffer();
  host.clear();
  resetStoreRevisionTracking();
  clearBackendConnection();
  services?.app.actions.setSidecarStatus("disconnected");
}
