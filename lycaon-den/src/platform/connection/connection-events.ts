import { resyncSourceViews } from "../../ui/paged-view/source-view-session.ts";
import { resyncAgentPresence, retainAgentPresenceProject } from "../../files/components/agent-presence-store.ts";
import { setWalkLoadSubscription } from "../../files/walk/walk-loader.ts";
import { requestWalkRefresh } from "../../files/walk/walk-store.ts";
import { watchWindowExit } from "../windows/watch-window-exit.ts";
import { refreshWalkPreviews } from "../../files/walk/walk-preview.ts";
import { refreshSessionSnapshot } from "../../api/session-snapshot-refresh.ts";
import { subscribeEvents, type EventOpenReason, type EventSubscription } from "../../api/events.ts";
import { requestSourceProjectionResync } from "../../files/source/source-events.ts";
import { resyncSourceOperations } from "../../store/source-operations.ts";
import { requestFileBriefingResync } from "../../files/components/file-briefing-live.ts";
import type { AttentionView, CLIOpenEvent, ProjectEvent, SessionEvent } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import type { EntityRetire } from "../../lifecycle/entity-retire.ts";
import { refreshPreflight } from "../persistence/preflight-store.ts";
import { ensureContributionFrame, invalidateContributionFrame } from "../../contributions/contribution-store.ts";
import { reconcileActiveScope } from "../../chat/session/session-reconcile.ts";
import { getBackendConnection } from "./backend.ts";
import { watchDocumentVisibilityResume } from "../visibility-resume.ts";
import type { ConnectionHost } from "./connection-host.ts";
import type { ConnectionInvalidation } from "./connection-invalidation.ts";
import type { createConnectionReconcile } from "./connection-reconcile.ts";
import { connectionSourceEvents } from "./connection-source-events.ts";
import { connectionSessionEvents } from "./connection-session-events.ts";

type EventPorts = {
  host: Pick<ConnectionHost, "connection" | "client" | "generation" | "assertCurrent" | "getClient">;
  invalidation: Pick<ConnectionInvalidation, "bind" | "cancel" | "settingsEvent" | "invalidate">;
  reconcile: Pick<ReturnType<typeof createConnectionReconcile>, "reconcileActiveShellProject" | "handleSessionGone" | "reconnect">;
  projects(): ProjectsStore | null;
  recents(): RecentsStore | null;
  retire(): EntityRetire | null;
};
export function createConnectionEvents(ports: EventPorts) {
  let eventSubscription: EventSubscription | null = null;
  let pendingEventCursor = "";
  let sseSubscribedProjectId: string | null = null;
  let stopVisibilityResume: (() => void) | undefined;
  let stopWindowExit: (() => void) | undefined;
  const projectEventListeners = new Set<(ev: ProjectEvent) => void>();
  const attentionEventListeners = new Set<(view: AttentionView) => void>();
  const attentionResyncListeners = new Set<() => void>();
  const cliOpenEventListeners = new Set<(ev: CLIOpenEvent) => void>();
  const sessionEventListeners = new Set<(ev: SessionEvent) => void>();
  /** Restart event delivery from a bootstrap boundary. */
  function resumeProjectEventsAfter(cursor: string): void {
    pendingEventCursor = cursor.trim();
    if (!pendingEventCursor || !eventSubscription) return;
    eventSubscription.resumeAfter(pendingEventCursor);
    pendingEventCursor = "";
  }

  function onProjectEvent(cb: (ev: ProjectEvent) => void): () => void {
    projectEventListeners.add(cb);
    return () => {
      projectEventListeners.delete(cb);
    };
  }

  /** Receives device-wide attention updates. */
  function onAttentionEvent(cb: (view: AttentionView) => void): () => void {
    attentionEventListeners.add(cb);
    return () => {
      attentionEventListeners.delete(cb);
    };
  }

  /** Receives device-wide CLI open events. */
  function onCLIOpenEvent(cb: (ev: CLIOpenEvent) => void): () => void {
    cliOpenEventListeners.add(cb);
    return () => {
      cliOpenEventListeners.delete(cb);
    };
  }

  /** A fresh snapshot covers events missed during disconnection. */
  function onAttentionResync(cb: () => void): () => void {
    attentionResyncListeners.add(cb);
    return () => {
      attentionResyncListeners.delete(cb);
    };
  }

  /** Receives session events for the subscribed project. */
  function onSessionEvent(cb: (ev: SessionEvent) => void): () => void {
    sessionEventListeners.add(cb);
    return () => {
      sessionEventListeners.delete(cb);
    };
  }

  function projectEventsAreSubscribed(projectId: string): boolean {
    const id = projectId.trim();
    return Boolean(id) && sseSubscribedProjectId === id && eventSubscription != null;
  }

  /** Wait for the active project's stream to connect. */
  async function waitForProjectEvents(projectId: string): Promise<boolean> {
    const id = projectId.trim();
    const subscription = eventSubscription;
    if (!id || sseSubscribedProjectId !== id || !subscription) return false;
    await subscription.ready;
    return sseSubscribedProjectId === id && eventSubscription === subscription;
  }

  function reset(): void {
    setWalkLoadSubscription(null);
    stopWindowExit?.();
    stopWindowExit = undefined;
    const previous = eventSubscription;
    eventSubscription = null;
    sseSubscribedProjectId = null;
    void previous?.close();
  }
  function dispose(): void {
    reset();
    stopVisibilityResume?.();
    stopVisibilityResume = undefined;
    ports.invalidation.cancel();
  }
  /** Subscribe to EventHub for a project UUID; replaces any prior subscription. */
  function subscribeProjectEvents(
    appStore: AppStore,
    projectId: string,
  ): void {
    const connection = getBackendConnection() ?? ports.host.connection;
    if (!connection?.apiToken) return;
    if (sseSubscribedProjectId === projectId && eventSubscription) return;
    setWalkLoadSubscription(null);

    void eventSubscription?.close();
    stopWindowExit?.();
    stopWindowExit = undefined;
    sseSubscribedProjectId = null;
    stopVisibilityResume?.();
    stopVisibilityResume = undefined;
    ports.invalidation.bind(appStore);

    retainAgentPresenceProject(projectId);
    const resyncPresence = () => {
      const client = ports.host.client;
      if (client) void resyncAgentPresence(client, projectId).catch(() => undefined);
    };

    let sourceResyncedBeforeOpen = false;
    const reconcileEventState = async () => {
      requestSourceProjectionResync(projectId);
      resyncSourceViews(projectId);
      sourceResyncedBeforeOpen = true;
      resyncPresence();
      const client = ports.host.client;
      if (!client) return;
      const generation = ports.host.generation;
      const registry = ports.projects();
      const projects = registry
        ? await registry.refresh(client)
        : await client.listProjects();
      ports.host.assertCurrent(generation, client);
      await ports.reconcile.reconcileActiveShellProject(appStore, client);
      ports.host.assertCurrent(generation, client);
      if (appStore.state.currentSession?.id) {
        await reconcileActiveScope(appStore, client, projects, {
          onSessionGone: ports.reconcile.handleSessionGone,
          resumeVisible: true,
          resumeEvents: false,
        });
      }
      ports.host.assertCurrent(generation, client);
      for (const listener of attentionResyncListeners) listener();
    };

    eventSubscription = subscribeEvents(connection, projectId, {
      project: (ev) => {
        requestWalkRefresh(ev.id);
        if (ev.action === "deleted") {
          ports.retire()?.project(ev.id);
          void ports.recents()?.removeRecentsForProject(ev.id);
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
          ports.reconcile.handleSessionGone({ projectId: ev.project_id, sessionId: ev.id });
          return;
        }
        const title = ev.title?.trim();
        const recents = ports.recents();
        if (!title || !recents) return;
        // Session events refresh recents without changing their order.
        void recents.registerSession({
          projectId: ev.project_id,
          sessionId: ev.id,
          title,
        });
      },
      ...connectionSourceEvents,
      ...connectionSessionEvents(appStore, ports.host.getClient),
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
        if (reason !== "reconnect" || !ports.host.client) return;
        void refreshPreflight();
        void ports.reconcile.reconnect(appStore);
      },
      onSettingsEvent: event => ports.invalidation.settingsEvent(appStore, event),
      onInvalidate: (keys, scope) => ports.invalidation.invalidate(appStore, keys, scope),
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
        if (!sessionId || !ports.host.client) return;
        void refreshSessionSnapshot(appStore, ports.host.client, sessionId).catch(() => undefined);
      },
    });
  }

  return { resumeProjectEventsAfter, onProjectEvent, onAttentionEvent, onCLIOpenEvent, onAttentionResync, onSessionEvent, projectEventsAreSubscribed, waitForProjectEvents, subscribeProjectEvents, reset, dispose };
}
