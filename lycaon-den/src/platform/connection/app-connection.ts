import type { LycaonClient } from "../../api/client.ts";
import { resetSessionEventRevisions } from "../../api/events.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ShellStore } from "../../store/shell-store.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
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
import { clearBackendConnection, discoverBackend, fetchHealth, getBackendConnection, readSidecarInfo, restartBackend, setBackendConnection } from "./backend.ts";
import { watchEngineState } from "./engine-supervision.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { noteHealthResponse, noteStoreRevision, resetStoreRevisionTracking } from "./health.ts";
import { incompatibleHost } from "./host-identity.ts";
import { setBackendReachabilityObserver } from "./request-connectivity.ts";
import { ConnectionHost } from "./connection-host.ts";
import { createConnectionReconcile } from "./connection-reconcile.ts";
import { ConnectionInvalidation } from "./connection-invalidation.ts";
import { createConnectionEvents } from "./connection-events.ts";

const ENGINE_RESTARTING_NOTICE = "engine_restarting";

const host = new ConnectionHost();
let settingsStoreRef: SettingsStore | null = null;
let costStoreRef: CostStore | null = null;
let recentsStoreRef: RecentsStore | null = null;
let shellStoreRef: ShellStore | null = null;
let projectsStoreRef: ProjectsStore | null = null;
let appStoreRef: AppStore | null = null;
let noticeStoreRef: NoticeStore | null = null;
let entityRetireRef: EntityRetire | null = null;
let connectInFlight: Promise<LycaonClient> | null = null;
/** OR-merged across concurrent connect callers for this in-flight attempt. */
let connectFailureNotices = false;

const invalidation = new ConnectionInvalidation({
  client: host.getClient,
  projects: () => projectsRegistry(),
  settings: () => settingsStoreRef,
  costs: () => costStoreRef,
});
const reconcile = createConnectionReconcile(host, {
  shell: () => shellStoreRef,
  projects: () => projectsStoreRef,
  recents: () => recentsStoreRef,
  retire: () => entityRetireRef,
  notices: appNoticeReporter,
  resetEvents: resetBackendEventStream,
  resubscribeForeground: resubscribeForegroundProjectEvents,
});
const events = createConnectionEvents({
  host, invalidation, reconcile,
  projects: () => projectsStoreRef,
  recents: () => recentsStoreRef,
  retire: () => entityRetireRef,
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

export function registerSettingsStore(store: SettingsStore): void {
  settingsStoreRef = store;
  invalidation.resetSettings();
}

export function registerCostStore(store: CostStore): void {
  costStoreRef = store;
}

/** Cost store for stage views mounted outside the store-prop tree. */
export function getCostStore(): CostStore | null {
  return costStoreRef;
}

export function registerRecentsStore(store: RecentsStore): void {
  recentsStoreRef = store;
}

export function registerShellStore(store: ShellStore): void {
  shellStoreRef = store;
}

export function registerProjectsStore(store: ProjectsStore): void {
  projectsStoreRef = store;
}

export function registerAppStore(store: AppStore): void {
  appStoreRef = store;
  setBackendReachabilityObserver({
    reachable: () => {
      const current = appStoreRef;
      if (current) markSidecarConnected(current);
    },
    unreachable: () => appStoreRef?.actions.setSidecarStatus("disconnected"),
  });
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

export function registerNoticeStore(store: NoticeStore): void {
  noticeStoreRef = store;
  // SSE dispatch uses a registration hook to avoid an import cycle.
  registerNoticePublisher(store);
}

export function registerEntityRetire(retire: EntityRetire | null): void {
  entityRetireRef = retire;
}

export function getEntityRetire(): EntityRetire | null {
  return entityRetireRef;
}

/** Notice store for code outside a reactive context. */
export function getRegisteredNoticeStore(): NoticeStore | null {
  return noticeStoreRef;
}

/** Reports app-scoped connection and boot failures. */
export function appNoticeReporter(): NoticeReporter {
  return noticeReporterFor(APP_SCOPE);
}

/** Returns a reporter bound to one notice scope. */
export function noticeReporterFor(scope: NoticeScope): NoticeReporter {
  const store = noticeStoreRef;
  if (!store) return { reportError: () => {}, publish: () => {} };
  return store.reporterFor(scope);
}

/** Resubscribe the foreground project after reconnect. */
function resubscribeForegroundProjectEvents(appStore = appStoreRef): void {
  const projectId = shellStoreRef?.state.foreground?.projectId?.trim() ?? "";
  if (!appStore) return;
  subscribeProjectEvents(appStore, projectId);
}

function resetBackendEventStream(appStore: AppStore | null): void {
  events.reset();
  costStoreRef?.actions.clear();
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
  const known = opts?.connection;
  if (known && host.client) {
    // The shell replaced the engine this window was bound to.
    disconnectAppBackend();
  } else if (host.client) {
    const client = host.client;
    const generation = host.generation;
    const valid = await verifyCachedBackend(appStore);
    host.assertCurrent(generation, client);
    if (valid) return client;
    disconnectAppBackend();
  }
  connectFailureNotices = opts?.reportFailure ?? false;
  const generation = host.generation;
  const discover = known
    ? async () => {
      setBackendConnection(known);
      return known;
    }
    : discoverBackend;
  const flight = connectAppBackendInner(appStore, generation, discover)
    .catch((err) => {
      if (generation === host.generation && connectFailureNotices) {
        appNoticeReporter().reportError(
          err instanceof Error ? err : new Error("Backend connection failed"),
        );
      }
      throw err;
    })
    .finally(() => {
      if (connectInFlight !== flight) return;
      connectInFlight = null;
      connectFailureNotices = false;
    });
  connectInFlight = flight;
  return flight;
}

/** Restart the managed sidecar and fully reconnect this window to the new store. */
export async function restartAppBackend(): Promise<LycaonClient> {
  const appStore = appStoreRef;
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

/**
 * Follows the engine the shell supervises. A new generation is a replacement
 * process with its own port and token, so the window rebinds to it; while
 * the engine is down the window holds its place instead of reporting each
 * failed request.
 */
export async function followEngineState(appStore: AppStore): Promise<() => void> {
  let sawEngineDown = false;
  return watchEngineState((engine) => {
    const notices = appNoticeReporter();
    if (engine.state === "restarting") {
      sawEngineDown = true;
      appStore.actions.setSidecarStatus("disconnected");
      notices.publish({
        code: ENGINE_RESTARTING_NOTICE,
        severity: "warning",
        title: CLIENT_NOTICES.engine_restarting.title,
        message: CLIENT_NOTICES.engine_restarting.message,
      });
      return;
    }
    noticeStoreRef?.withdraw(ENGINE_RESTARTING_NOTICE, APP_SCOPE);
    if (engine.state === "stopped") {
      sawEngineDown = true;
      appStore.actions.setSidecarStatus("disconnected");
      return;
    }
    if (engine.state !== "running" || connectInFlight) return;
    const bound = boundEngineGeneration();
    // An unbound window that never lost an engine is still booting; its own connect binds it.
    if (bound === engine.generation || (bound === undefined && !sawEngineDown)) return;
    sawEngineDown = false;
    void readSidecarInfo()
      .then((connection) => {
        if (!connection || connection.engineGeneration !== engine.generation) return;
        if (boundEngineGeneration() === engine.generation) return;
        return connectAppBackend(appStore, { connection });
      })
      .catch(() => undefined);
  });
}

export function disconnectAppBackend(): void {
  host.advance();
  resetBackendEventStream(appStoreRef);
  events.dispose();
  cancelWorkerTranscriptCoalesce();
  clearChildMessageBuffer();
  connectInFlight = null;
  connectFailureNotices = false;
  host.clear();
  resetStoreRevisionTracking();
  clearBackendConnection();
  appStoreRef?.actions.setSidecarStatus("disconnected");
}
