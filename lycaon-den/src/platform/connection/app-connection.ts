import { receiveSourceViewEvent, resyncSourceViews } from "../../ui/paged-view/source-view-session.ts";
import {
  applyAgentPresenceEvent,
  resyncAgentPresence,
  retainAgentPresenceProject,
} from "../../files/components/agent-presence-store.ts";
import { setWalkLoadSubscription } from "../../files/walk/walk-loader.ts";
import { requestWalkRefresh } from "../../files/walk/walk-store.ts";
import { watchWindowExit } from "../windows/watch-window-exit.ts";
import { refreshWalkPreviews } from "../../files/walk/walk-preview.ts";
import type { LycaonClient } from "../../api/client.ts";
import { refreshSessionSnapshot } from "../../api/session-snapshot-refresh.ts";
import { createLycaonClient } from "../../api/client-impl.ts";
import { LycaonApiError } from "../../api/http.ts";
import {
  resetSessionEventRevisions,
  subscribeEvents,
  type EventOpenReason,
  type EventSubscription,
} from "../../api/events.ts";
import { requestSourceProjectionResync, applySourceChangesEvent } from "../../files/source/source-events.ts";
import { applySourceOperationEvent, resyncSourceOperations } from "../../store/source-operations.ts";

import {
  applyFileBriefingEvent,
  requestFileBriefingResync,
} from "../../files/components/file-briefing-live.ts";
import { receiveEditorDocumentEvent } from "../../files/documents/editor-document.ts";
import { notifyArtifactChanged } from "../../chat/visual/artifact-change-store.ts";
import type {
  AttentionView,
  CLIOpenEvent,
  ProjectEvent,
  SessionEvent,
  SettingsArea,
} from "../../api/types.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { valueOf } from "../../store/load-state.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ShellStore } from "../../store/shell-store.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import type { NoticeReporter, NoticeStore } from "../../notices/notice-store.ts";
import { registerNoticePublisher } from "../../notices/notice-store.ts";
import type { EntityRetire } from "../../lifecycle/entity-retire.ts";
import { refreshPreflight, resetPreflightStore } from "../persistence/preflight-store.ts";
import {
  ensureContributionFrame,
  invalidateContributionFrame,
} from "../../contributions/contribution-store.ts";
import type { NoticeScope } from "../../notices/notice-scope.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import type { StoreInvalidation } from "../../api/events.ts";
import {
  createSettingsInvalidationScheduler,
  type SettingsInvalidationScheduler,
} from "../../settings/settings-invalidation.ts";
import type { SettingsInvalidationSlice } from "../../settings/settings-actions.ts";
import {
  fileSummariesEnabled,
  fileSummariesSettingKnown,
  refreshFileSummariesSetting,
  resetFileSummariesSetting,
} from "../../settings/editor/file-summary-settings.ts";
import { invalidateApprovalGrantsCache } from "../../settings/security/approval-grants-cache.ts";
import {
  refreshModelPolicy,
  refreshProviders,
  refreshProviderKinds,
  refreshPricing,
} from "../../settings/settings-actions.ts";
import { costTrackingEnabled } from "../../cost/cost-tracking.ts";
import type { CostStore } from "../../store/cost-store.ts";
import { refreshWorkflowState } from "../../chat/workflow/workflow-actions.ts";
import { refreshFindings } from "../../chat/actions/findings-actions.ts";
import { refreshProgress } from "../../chat/progress/progress-actions.ts";
import { refreshQueue } from "../../chat/actions/queue-actions.ts";
import { createSessionInvalidationSchedulers } from "../../chat/session/session-invalidation.ts";
import { cancelWorkerTranscriptCoalesce } from "../../chat/worker/worker-transcript-coalesce.ts";
import { clearChildMessageBuffer } from "../../chat/transcript/projection/worker-child-message-buffer.ts";
import {
  reconcileActiveScope,
  reconcileRecentsWithStores,
  refreshCodeScanCache,
} from "../../chat/session/session-reconcile.ts";
import type { SessionScope } from "../../chat/session/session-scope.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import type { BackendConnection } from "./backend.ts";
import {
  clearBackendConnection,
  discoverBackend,
  fetchHealth,
  getBackendConnection,
  restartBackend,
  setBackendConnection,
} from "./backend.ts";
import { isTauriRuntime } from "../runtime.ts";
import {
  noteHealthResponse,
  noteStoreRevision,
  resetStoreRevisionTracking,
} from "./health.ts";
import { incompatibleHost, noteHostInfo, type HostInfoNote } from "./host-identity.ts";
import { setBackendReachabilityObserver } from "./request-connectivity.ts";
import { watchDocumentVisibilityResume } from "../visibility-resume.ts";

let cachedClient: LycaonClient | null = null;
let backendGeneration = 0;
let cachedConnection: BackendConnection | null = null;
let eventSubscription: EventSubscription | null = null;
let pendingEventCursor = "";
/** Project id passed to the live SSE reader — not shell.activeProjectId. */
let sseSubscribedProjectId: string | null = null;
let settingsStoreRef: SettingsStore | null = null;
let costStoreRef: CostStore | null = null;
let recentsStoreRef: RecentsStore | null = null;
let shellStoreRef: ShellStore | null = null;
let projectsStoreRef: ProjectsStore | null = null;
let appStoreRef: AppStore | null = null;
let noticeStoreRef: NoticeStore | null = null;
let entityRetireRef: EntityRetire | null = null;
let settingsInvalidation: SettingsInvalidationScheduler | null = null;
let sessionInvalidation: ReturnType<
  typeof createSessionInvalidationSchedulers
> | null = null;
const projectEventListeners = new Set<(ev: ProjectEvent) => void>();
const attentionEventListeners = new Set<(view: AttentionView) => void>();
const attentionResyncListeners = new Set<() => void>();
const cliOpenEventListeners = new Set<(ev: CLIOpenEvent) => void>();
const sessionEventListeners = new Set<(ev: SessionEvent) => void>();
let connectInFlight: Promise<LycaonClient> | null = null;
/** OR-merged across concurrent connect callers for this in-flight attempt. */
let connectFailureNotices = false;
let stopVisibilityResume: (() => void) | undefined;
let stopWindowExit: (() => void) | undefined;

/** Restart event delivery from a bootstrap boundary. */
export function resumeProjectEventsAfter(cursor: string): void {
  pendingEventCursor = cursor.trim();
  if (!pendingEventCursor || !eventSubscription) return;
  eventSubscription.resumeAfter(pendingEventCursor);
  pendingEventCursor = "";
}

export function registerSettingsStore(store: SettingsStore): void {
  settingsStoreRef = store;
  settingsInvalidation?.cancel();
  settingsInvalidation = null;
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
  if (cachedClient && !fileSummariesSettingKnown()) {
    void refreshFileSummariesSetting(cachedClient).catch(() => undefined);
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

/** Read-only project registry for actions that resolve path → id. */
export function projectsRegistry(): readonly Project[] {
  return projectsStoreRef?.state.projects ?? [];
}

export function onProjectEvent(cb: (ev: ProjectEvent) => void): () => void {
  projectEventListeners.add(cb);
  return () => {
    projectEventListeners.delete(cb);
  };
}

/** Receives device-wide attention updates. */
export function onAttentionEvent(cb: (view: AttentionView) => void): () => void {
  attentionEventListeners.add(cb);
  return () => {
    attentionEventListeners.delete(cb);
  };
}

/** Receives device-wide CLI open events. */
export function onCLIOpenEvent(cb: (ev: CLIOpenEvent) => void): () => void {
  cliOpenEventListeners.add(cb);
  return () => {
    cliOpenEventListeners.delete(cb);
  };
}

/** A fresh snapshot covers events missed during disconnection. */
export function onAttentionResync(cb: () => void): () => void {
  attentionResyncListeners.add(cb);
  return () => {
    attentionResyncListeners.delete(cb);
  };
}

/** Receives session events for the subscribed project. */
export function onSessionEvent(cb: (ev: SessionEvent) => void): () => void {
  sessionEventListeners.add(cb);
  return () => {
    sessionEventListeners.delete(cb);
  };
}

export function getLycaonClient(): LycaonClient | null {
  return cachedClient;
}

export function setLycaonClientForTest(client: LycaonClient | null): void {
  cachedClient = client;
}

function resetBackendEventStream(appStore: AppStore | null): void {
  setWalkLoadSubscription(null);
  stopWindowExit?.();
  stopWindowExit = undefined;
  const previous = eventSubscription;
  eventSubscription = null;
  sseSubscribedProjectId = null;
  void previous?.close();
  costStoreRef?.actions.clear();
  resetPreflightStore();
  resetFileSummariesSetting();
  if (appStore) {
    appStore.actions.invalidateSessionViewRequests();
    resetSessionEventRevisions(appStore.actions);
  }
}

function assertCurrentBackend(generation: number, client?: LycaonClient): void {
  if (generation !== backendGeneration || (client && cachedClient !== client)) {
    throw new DOMException("Backend connection changed.", "AbortError");
  }
}

/** Reads the host handshake that every other route is trusted against. */
async function readHostHandshake(
  client: LycaonClient,
  generation: number,
): Promise<HostInfoNote> {
  const info = await client.getHost();
  assertCurrentBackend(generation, client);
  return noteHostInfo(info);
}

/** Attaches a known backend without managing its lifecycle. */
export function attachKnownBackend(
  appStore: AppStore,
  connection: BackendConnection,
): LycaonClient {
  backendGeneration++;
  connectInFlight = null;
  connectFailureNotices = false;
  resetBackendEventStream(appStore);
  setBackendConnection(connection);
  cachedConnection = connection;
  const client = createLycaonClient(connection);
  cachedClient = client;
  void invalidateContributionFrame();
  markSidecarConnected(appStore);
  void readHostHandshake(client, backendGeneration).catch(() => undefined);
  return client;
}

export type ConnectAppBackendOpts = {
  /** Reports connection errors for explicit retries. */
  reportFailure?: boolean;
};

/** The host answered that a chat is gone: retire it and leave its conversation. */
export function handleSessionGone(scope: SessionScope): void {
  entityRetireRef?.session(scope);
  shellStoreRef?.evictConversation(scope.projectId, scope.sessionId);
}

async function reconcileAfterStoreRevisionChange(
  appStore: AppStore,
  client: LycaonClient,
): Promise<void> {
  const generation = backendGeneration;
  assertCurrentBackend(generation, client);
  resetBackendEventStream(appStore);
  if (recentsStoreRef) {
    await reconcileRecentsWithStores(
      client,
      recentsStoreRef,
      shellStoreRef ?? undefined,
      () => generation === backendGeneration && cachedClient === client,
    ).catch(() => undefined);
  }
  assertCurrentBackend(generation, client);
  if (appStore.state.currentSession?.id) {
    await reconcileActiveScope(appStore, client, projectsRegistry(), {
      onSessionGone: handleSessionGone,
      resumeVisible: true,
    }).catch(() => undefined);
  }
  assertCurrentBackend(generation, client);
  resubscribeForegroundProjectEvents(appStore);
  void refreshPreflight();
  void refreshFileSummariesSetting(client).catch(() => undefined);
}

/** Drops shell scope absent from the live registry. */
function evictStaleShellScope(appStore: AppStore): void {
  const projectId = shellStoreRef?.state.activeProjectId?.trim();
  if (!projectId) return;
  if (projectsStoreRef?.byId(projectId)) return;
  appStore.actions.resetChatForSessionSwitch();
  shellStoreRef?.clearToHome();
  void persistAppStateInBackground({
    lastActiveProjectId: undefined,
    lastSessionSnapshot: undefined,
    cachedProjects: undefined,
  });
}

function isProjectNotFound(err: unknown): boolean {
  return err instanceof LycaonApiError && err.code === "project_not_found";
}

/** Re-fetch the active shell project when a listProjects snapshot omitted it. */
async function reconcileActiveShellProject(
  appStore: AppStore,
  client: LycaonClient,
): Promise<void> {
  const projectId = shellStoreRef?.state.activeProjectId?.trim();
  if (!projectId || projectsStoreRef?.byId(projectId)) return;
  const generation = backendGeneration;
  const epoch = appStore.state.sessionViewEpoch;
  const isCurrent = () => generation === backendGeneration && cachedClient === client &&
    appStore.state.sessionViewEpoch === epoch && shellStoreRef?.state.activeProjectId?.trim() === projectId;
  if (!isCurrent()) return;
  try {
    const project = await client.getProject(projectId);
    if (isCurrent()) projectsStoreRef?.upsert(project);
  } catch (err) {
    if (isCurrent() && isProjectNotFound(err)) {
      evictStaleShellScope(appStore);
    }
  }
}

async function syncProjectsFromBackend(
  appStore: AppStore,
  client: LycaonClient,
): Promise<readonly Project[]> {
  const generation = backendGeneration;
  assertCurrentBackend(generation, client);
  let revisionChanged = false;
  const connection = getBackendConnection() ?? cachedConnection;
  // An empty base URL selects the same-origin harness proxy.
  if (connection) {
    try {
      const health = await fetchHealth(connection.baseUrl);
      assertCurrentBackend(generation, client);
      revisionChanged = noteStoreRevision(health.store_revision);
      const skew = noteHealthResponse(health);
      if (skew) appNoticeReporter().publish(skew);
      // Recovery mode exposes no project or session routes.
      if (health.status === "recovery") {
        return projectsStoreRef?.state.projects ?? [];
      }
    } catch {
      assertCurrentBackend(generation, client);
      if (!isTauriRuntime()) throw new Error("Health check failed");
    }
  }

  const host = await readHostHandshake(client, generation);
  // An incompatible host is presented as a critical stop; none of its routes are used.
  if (!host.compatible) {
    resetBackendEventStream(appStore);
    return projectsStoreRef?.state.projects ?? [];
  }
  // Another install shares nothing with cached state.
  revisionChanged ||= host.hostChanged;

  if (revisionChanged) resetBackendEventStream(appStore);
  const projects = projectsStoreRef
    ? await projectsStoreRef.refresh(client)
    : await client.listProjects();
  assertCurrentBackend(generation, client);
  await reconcileActiveShellProject(appStore, client);
  assertCurrentBackend(generation, client);

  if (revisionChanged) {
    void persistAppStateInBackground({ lastSessionSnapshot: undefined });
    await reconcileAfterStoreRevisionChange(appStore, client);
    assertCurrentBackend(generation, client);
  }
  return projects;
}

async function verifyCachedBackend(appStore: AppStore): Promise<boolean> {
  const client = cachedClient;
  if (!client) return false;
  try {
    const generation = backendGeneration;
    await syncProjectsFromBackend(appStore, client);
    assertCurrentBackend(generation, client);
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
  if (cachedClient) {
    const client = cachedClient;
    const generation = backendGeneration;
    const valid = await verifyCachedBackend(appStore);
    assertCurrentBackend(generation, client);
    if (valid) return client;
    disconnectAppBackend();
  }
  connectFailureNotices = opts?.reportFailure ?? false;
  const generation = backendGeneration;
  const flight = connectAppBackendInner(appStore, generation)
    .catch((err) => {
      if (generation === backendGeneration && connectFailureNotices) {
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
  const generation = backendGeneration;
  await restartBackend();
  assertCurrentBackend(generation);
  return connectAppBackend(appStore, { reportFailure: true });
}

async function connectAppBackendInner(appStore: AppStore, generation: number): Promise<LycaonClient> {
  appStore.actions.setSidecarStatus("connecting");
  try {
    const connection = await discoverBackend();
    assertCurrentBackend(generation);
    resetBackendEventStream(appStore);
    cachedConnection = connection;
    const client = createLycaonClient(connection);
    cachedClient = client;
    // Client discovery may finish after hydration begins.
    void invalidateContributionFrame();
    appStore.actions.setLoading(true);

    // Health selects recovery before project routes are called.
    try {
      const health = await fetchHealth(connection.baseUrl);
      assertCurrentBackend(generation, client);
      const skew = noteHealthResponse(health);
      if (skew) appNoticeReporter().publish(skew);
      if (health.status === "recovery") {
        noteStoreRevision(health.store_revision);
        markSidecarConnected(appStore);
        return client;
      }
    } catch {
      assertCurrentBackend(generation, client);
      /* Project synchronization performs the health probe. */
    }

    await syncProjectsFromBackend(appStore, client);
    assertCurrentBackend(generation, client);
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
        shouldApply: () => generation === backendGeneration && cachedClient === client,
      });
      assertCurrentBackend(generation, client);
    }

    if (settingsStoreRef) {
      try {
        // Independent settings slices hydrate concurrently.
        await Promise.all([
          refreshProviders(settingsStoreRef, client, () => generation === backendGeneration && cachedClient === client),
          refreshProviderKinds(settingsStoreRef, client, () => generation === backendGeneration && cachedClient === client),
          refreshModelPolicy(settingsStoreRef, client, projectsRegistry(), () => generation === backendGeneration && cachedClient === client),
          refreshPricing(settingsStoreRef, client, () => generation === backendGeneration && cachedClient === client),
        ]);
      } catch {
        /* settings hydrate is best-effort on connect */
      }
    }
    assertCurrentBackend(generation, client);
    return client;
  } catch (err) {
    if (generation !== backendGeneration) throw err;
    cachedClient = null;
    cachedConnection = null;
    clearBackendConnection();
    appStore.actions.setSidecarStatus("disconnected");
    throw err;
  } finally {
    if (generation === backendGeneration) appStore.actions.setLoading(false);
  }
}

export function projectEventsAreSubscribed(projectId: string): boolean {
  const id = projectId.trim();
  return Boolean(id) && sseSubscribedProjectId === id && eventSubscription != null;
}

/** Wait for the active project's stream to connect. */
export async function waitForProjectEvents(projectId: string): Promise<boolean> {
  const id = projectId.trim();
  const subscription = eventSubscription;
  if (!id || sseSubscribedProjectId !== id || !subscription) return false;
  await subscription.ready;
  return sseSubscribedProjectId === id && eventSubscription === subscription;
}

type SettingsAreaEffect = {
  slices?: readonly SettingsInvalidationSlice[];
  apply?: (appStore: AppStore, client: LycaonClient) => void;
};

const SETTINGS_AREA_EFFECTS: Readonly<Record<SettingsArea, SettingsAreaEffect>> = {
  approvals: {
    slices: ["approvals"],
    apply: (appStore) => {
      invalidateApprovalGrantsCache();
      appStore.actions.bumpApprovalsRevision();
    },
  },
  extensions: {
    apply: (appStore) => {
      appStore.actions.bumpExtensionsRevision();
      void invalidateContributionFrame();
    },
  },
  file_summaries: {
    apply: (_appStore, client) => void refreshFileSummariesSetting(client).catch(() => undefined),
  },
  limits: { slices: ["limits"] },
  mcp: { apply: () => void invalidateContributionFrame() },
  power: {},
  pricing: { slices: ["pricing"] },
  project_trust: { apply: (appStore) => appStore.actions.bumpProjectTrustRevision() },
  review: { slices: ["review"] },
  security_scanners: {},
  verify: { apply: (appStore) => appStore.actions.bumpVerifyDetectRevision() },
  web_research: {},
};

/** Subscribe to EventHub for a project UUID; replaces any prior subscription. */
export function subscribeProjectEvents(
  appStore: AppStore,
  projectId: string,
): void {
  const connection = getBackendConnection() ?? cachedConnection;
  if (!connection?.apiToken) return;
  if (sseSubscribedProjectId === projectId && eventSubscription) return;
  setWalkLoadSubscription(null);

  void eventSubscription?.close();
  stopWindowExit?.();
  stopWindowExit = undefined;
  sseSubscribedProjectId = null;
  stopVisibilityResume?.();
  stopVisibilityResume = undefined;
  settingsInvalidation?.cancel();
  sessionInvalidation?.cancel();

  sessionInvalidation = createSessionInvalidationSchedulers(
    appStore,
    () => cachedClient,
    projectsRegistry,
    () => (costStoreRef?.state.liveConsumers ?? 0) > 0,
  );

  if (settingsStoreRef) {
    settingsInvalidation = createSettingsInvalidationScheduler(
      settingsStoreRef,
      getLycaonClient,
      projectsRegistry,
      () => appStore.state.currentSession?.workspace_path,
    );
  }

  retainAgentPresenceProject(projectId);
  const resyncPresence = () => {
    const client = cachedClient;
    if (client) void resyncAgentPresence(client, projectId).catch(() => undefined);
  };

  let sourceResyncedBeforeOpen = false;
  const reconcileEventState = async () => {
    requestSourceProjectionResync(projectId);
    resyncSourceViews(projectId);
    sourceResyncedBeforeOpen = true;
    resyncPresence();
    const client = cachedClient;
    if (!client) return;
    const generation = backendGeneration;
    const projects = projectsStoreRef
      ? await projectsStoreRef.refresh(client)
      : await client.listProjects();
    assertCurrentBackend(generation, client);
    await reconcileActiveShellProject(appStore, client);
    assertCurrentBackend(generation, client);
    if (appStore.state.currentSession?.id) {
      await reconcileActiveScope(appStore, client, projects, {
        onSessionGone: handleSessionGone,
        resumeVisible: true,
        resumeEvents: false,
      });
    }
    assertCurrentBackend(generation, client);
    for (const listener of attentionResyncListeners) listener();
  };

  eventSubscription = subscribeEvents(connection, projectId, {
    project: (ev) => {
      requestWalkRefresh(ev.id);
      if (ev.action === "deleted") {
        entityRetireRef?.project(ev.id);
        void recentsStoreRef?.removeRecentsForProject(ev.id);
      }
      for (const listener of projectEventListeners) {
        listener(ev);
      }
    },
    attention: (view) => {
      for (const listener of attentionEventListeners) {
        listener(view);
      }
    },
    cli_open: (ev) => {
      for (const listener of cliOpenEventListeners) {
        listener(ev);
      }
    },
    session: (ev) => {
      requestWalkRefresh(ev.project_id);
      refreshWalkPreviews(ev.project_id, ev.id);
      for (const listener of sessionEventListeners) {
        listener(ev);
      }
      if (ev.action === "deleted") {
        handleSessionGone({ projectId: ev.project_id, sessionId: ev.id });
        return;
      }
      const title = ev.title?.trim();
      if (!title || !recentsStoreRef) return;
      // Session events refresh recents without changing their order.
      void recentsStoreRef.registerSession({
        projectId: ev.project_id,
        sessionId: ev.id,
        title,
      });
    },
    findings: (ev, scope) => {
      const sessionId = scope.kind === "session" ? scope.session_id : undefined;
      if (sessionId !== appStore.state.currentSession?.id) return;
      if (!sessionId || !cachedClient) return;
      const current = valueOf(appStore.state.findings)?.revision ?? 0;
      if (ev.revision <= current) return;
      void refreshFindings(appStore, cachedClient, sessionId, ev.revision);
    },
    source_view: (ev) => receiveSourceViewEvent(ev),
    source_changed: (ev) => {
      applySourceChangesEvent(ev);
    },
    source_operation: (ev) => {
      applySourceOperationEvent(ev);
    },
    file_briefing: (ev) => {
      if (fileSummariesSettingKnown() && fileSummariesEnabled()) {
        applyFileBriefingEvent(ev);
      }
    },
    editor_document: (ev) => {
      receiveEditorDocumentEvent(ev);
    },
    agent_presence: (ev) => {
      applyAgentPresenceEvent(ev);
    },
    artifact: (ev) => {
      notifyArtifactChanged(ev);
    },
    progress: (ev, scope) => {
      const sessionId = scope.kind === "session" ? scope.session_id : undefined;
      if (sessionId !== appStore.state.currentSession?.id) return;
      if (!sessionId || !cachedClient) return;
      const current = appStore.state.progress?.revision ?? 0;
      if (ev.revision <= current) return;
      void refreshProgress(appStore, cachedClient, sessionId, ev.revision).catch(
        () => undefined,
      );
    },
    queue: (ev, scope) => {
      const sessionId = scope.kind === "session" ? scope.session_id : undefined;
      if (sessionId !== appStore.state.currentSession?.id) return;
      if (!sessionId || !cachedClient) return;
      const current = appStore.state.queueDraft?.revision ?? 0;
      if (ev.revision <= current) return;
      void refreshQueue(appStore, cachedClient, sessionId, ev.revision).catch(
        () => undefined,
      );
    },
  }, {
    appStore,
    storeActions: appStore.actions,
    onReconcile: reconcileEventState,
    onError: () => setWalkLoadSubscription(null),
    onReconnectAttempt: () => setWalkLoadSubscription(null),
    onOpen: (reason: EventOpenReason) => {
      setWalkLoadSubscription(projectId);
      if (!sourceResyncedBeforeOpen) {
        requestSourceProjectionResync(projectId);
        resyncSourceViews(projectId);
      }
      // Transitions before this stream opened reached no handler.
      if (reason !== "resume") resyncSourceOperations();
      sourceResyncedBeforeOpen = false;
      requestWalkRefresh(projectId);
      refreshWalkPreviews(projectId);
      if (reason === "resume") return;
      // Presence is held in host memory, so a fresh stream may follow a host restart.
      resyncPresence();
      appStore.actions.setSidecarStatus("connected");
      requestFileBriefingResync();
      for (const listener of attentionResyncListeners) listener();
      void (reason === "reconnect"
        ? invalidateContributionFrame()
        : ensureContributionFrame());
      if (reason !== "reconnect" || !cachedClient) return;
      void refreshPreflight();
      const client = cachedClient;
      const generation = backendGeneration;
      const connection = getBackendConnection() ?? cachedConnection;
      void (async () => {
        try {
          let revisionChanged = false;
          let recovery = false;
          if (connection) {
            const health = await fetchHealth(connection.baseUrl);
            revisionChanged = noteStoreRevision(health.store_revision);
            const skew = noteHealthResponse(health);
            if (skew) appNoticeReporter().publish(skew);
            recovery = health.status === "recovery";
          }
          if (recovery) {
            return;
          }
          const host = await readHostHandshake(client, generation);
          if (!host.compatible) {
            resetBackendEventStream(appStore);
            return;
          }
          if (revisionChanged || host.hostChanged) {
            await reconcileAfterStoreRevisionChange(appStore, client);
          } else if (appStore.state.currentSession?.id) {
            await reconcileActiveScope(appStore, client, projectsRegistry(), {
              onSessionGone: handleSessionGone,
              resumeVisible: true,
            }).catch(() => undefined);
          }
        } catch {
          /* reconnect reconcile is best-effort */
        }
      })();
    },
    onSettingsEvent: (event) => {
      if (!cachedClient) return;
      const effect = SETTINGS_AREA_EFFECTS[event.area];
      if (effect.slices) settingsInvalidation?.schedule(effect.slices);
      effect.apply?.(appStore, cachedClient);
    },
    onInvalidate: (keys: readonly StoreInvalidation[], scope) => {
      const sessionId = appStore.state.currentSession?.id;
      const projectDir = appStore.state.currentSession?.workspace_path;
      if (!cachedClient) return;
      if (
        settingsInvalidation &&
        (keys.includes("providers") || keys.includes("model_policy"))
      ) {
        const slices: SettingsInvalidationSlice[] = [];
        if (keys.includes("providers")) slices.push("providers");
        if (keys.includes("model_policy")) slices.push("model_policy");
        settingsInvalidation.schedule(slices);
      }
      // Readiness invalidations refresh one coherent report.
      if (keys.includes("providers") || keys.includes("readiness")) {
        void refreshPreflight();
      }
      if (keys.includes("providers") || keys.includes("model_policy")) {
        appStore.actions.bumpModelPolicyRevision();
      }
      if (scope.kind === "session" && scope.session_id !== sessionId) return;
      if (!projectDir) return;
      if (keys.includes("workers") && sessionId) {
        sessionInvalidation?.scheduleWorkers(projectDir, sessionId);
      }
      if (keys.includes("workflows") && sessionId) {
        const client = cachedClient;
        sessionInvalidation?.scheduleWorkflow(() =>
          refreshWorkflowState(
            appStore,
            client,
            sessionId,
            projectDir,
            projectsRegistry(),
          ).catch(() => undefined),
        );
      }
      if (keys.includes("session") && sessionId) {
        // Busy and idle transitions refresh the progress clock.
        const client = cachedClient;
        sessionInvalidation?.scheduleProgress(async () => {
          await refreshProgress(appStore, client, sessionId).catch(
            () => undefined,
          );
        });
      }
      if (keys.includes("board") && sessionId) {
        sessionInvalidation?.scheduleBoard(
          projectDir,
          sessionId,
          keys.includes("cost"),
        );
      }
      if (
        keys.includes("cost") &&
        costStoreRef &&
        settingsStoreRef &&
        costTrackingEnabled(settingsStoreRef)
      ) {
        const client = cachedClient;
        const costStore = costStoreRef;
        const costSessionId = sessionId;
        sessionInvalidation?.scheduleCost(async () => {
          if (costSessionId) {
            await costStore.refreshSession(client, costSessionId).catch(
              () => undefined,
            );
          }
          // Self-fetching surfaces refetch here.
          costStore.emitInvalidated();
        });
      }
      if (keys.includes("scan")) {
        void refreshCodeScanCache(appStore, cachedClient).catch(
          () => undefined,
        );
      }
    },
  });
  if (pendingEventCursor) {
    eventSubscription.resumeAfter(pendingEventCursor);
    pendingEventCursor = "";
  }

  sseSubscribedProjectId = projectId;

  stopWindowExit = watchWindowExit(() => undefined, {
    beforeDestroy: async () => {
      setWalkLoadSubscription(null);
      const subscription = eventSubscription;
      eventSubscription = null;
      sseSubscribedProjectId = null;
      await subscription?.close();
    },
    onKeepOpen: () => {
      if (!eventSubscription) subscribeProjectEvents(appStore, projectId);
    },
  });
  stopVisibilityResume = watchDocumentVisibilityResume({
    // Visibility retries disconnected streams immediately.
    isReconnecting: () =>
      appStore.state.sidecarStatus === "reconnecting" ||
      appStore.state.sidecarStatus === "disconnected",
    wakeReconnect: () => eventSubscription?.wakeReconnect(),
    onVisible: () => {
      const sessionId = appStore.state.currentSession?.id;
      if (!sessionId || !cachedClient) return;
      void refreshSessionSnapshot(appStore, cachedClient, sessionId).catch(() => undefined);
    },
  });
}

export function disconnectAppBackend(): void {
  backendGeneration++;
  resetBackendEventStream(appStoreRef);
  stopVisibilityResume?.();
  stopVisibilityResume = undefined;
  settingsInvalidation?.cancel();
  settingsInvalidation = null;
  sessionInvalidation?.cancel();
  sessionInvalidation = null;
  cancelWorkerTranscriptCoalesce();
  clearChildMessageBuffer();
  connectInFlight = null;
  connectFailureNotices = false;
  cachedClient = null;
  cachedConnection = null;
  resetStoreRevisionTracking();
  clearBackendConnection();
  appStoreRef?.actions.setSidecarStatus("disconnected");
}
