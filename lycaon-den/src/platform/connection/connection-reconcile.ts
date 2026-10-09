import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ShellStore } from "../../store/shell-store.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import type { NoticeReporter } from "../../notices/notice-store.ts";
import type { EntityRetire } from "../../lifecycle/entity-retire.ts";
import { refreshPreflight } from "../persistence/preflight-store.ts";
import { refreshFileSummariesSetting } from "../../settings/editor/file-summary-settings.ts";
import { reconcileActiveScope, reconcileRecentsWithStores } from "../../chat/session/session-reconcile.ts";
import type { SessionScope } from "../../chat/session/session-scope.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import { fetchHealth, getBackendConnection } from "./backend.ts";
import { isTauriRuntime } from "../runtime.ts";
import { noteHealthResponse, noteStoreRevision } from "./health.ts";
import type { ConnectionHost } from "./connection-host.ts";
type ReconcilePorts = {
  shell(): ShellStore | null;
  projects(): ProjectsStore | null;
  recents(): RecentsStore | null;
  retire(): EntityRetire | null;
  notices(): NoticeReporter;
  resetEvents(appStore: AppStore): void;
  resubscribeForeground(appStore: AppStore): void;
};
export function createConnectionReconcile(host: Pick<ConnectionHost, "generation" | "client" | "connection" | "assertCurrent" | "handshake">, ports: ReconcilePorts) {
  /** Read-only project registry for actions that resolve path → id. */
  function projectsRegistry(): readonly Project[] {
    return ports.projects()?.state.projects ?? [];
  }

  /** The host answered that a chat is gone: retire it and leave its conversation. */
  function handleSessionGone(scope: SessionScope): void {
    ports.retire()?.session(scope);
    ports.shell()?.evictConversation(scope.projectId, scope.sessionId);
  }

  async function reconcileAfterStoreRevisionChange(
    appStore: AppStore,
    client: LycaonClient,
  ): Promise<void> {
    const generation = host.generation;
    host.assertCurrent(generation, client);
    ports.resetEvents(appStore);
    const recents = ports.recents();
    if (recents) {
      await reconcileRecentsWithStores(
        client,
        recents,
        ports.shell() ?? undefined,
        () => generation === host.generation && host.client === client,
      ).catch(() => undefined);
    }
    host.assertCurrent(generation, client);
    if (appStore.state.currentSession?.id) {
      await reconcileActiveScope(appStore, client, projectsRegistry(), {
        onSessionGone: handleSessionGone,
        resumeVisible: true,
      }).catch(() => undefined);
    }
    host.assertCurrent(generation, client);
    ports.resubscribeForeground(appStore);
    void refreshPreflight();
    void refreshFileSummariesSetting(client).catch(() => undefined);
  }

  /** Drops shell scope absent from the live registry. */
  function evictStaleShellScope(appStore: AppStore): void {
    const projectId = ports.shell()?.state.activeProjectId?.trim();
    if (!projectId) return;
    if (ports.projects()?.byId(projectId)) return;
    appStore.actions.resetChatForSessionSwitch();
    ports.shell()?.clearToHome();
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
    const projectId = ports.shell()?.state.activeProjectId?.trim();
    if (!projectId || ports.projects()?.byId(projectId)) return;
    const generation = host.generation;
    const epoch = appStore.state.sessionViewEpoch;
    const isCurrent = () => generation === host.generation && host.client === client &&
      appStore.state.sessionViewEpoch === epoch && ports.shell()?.state.activeProjectId?.trim() === projectId;
    if (!isCurrent()) return;
    try {
      const project = await client.getProject(projectId);
      if (isCurrent()) ports.projects()?.upsert(project);
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
    const generation = host.generation;
    host.assertCurrent(generation, client);
    let revisionChanged = false;
    const connection = getBackendConnection() ?? host.connection;
    // An empty base URL selects the same-origin harness proxy.
    if (connection) {
      try {
        const health = await fetchHealth(connection.baseUrl);
        host.assertCurrent(generation, client);
        revisionChanged = noteStoreRevision(health.store_revision);
        const skew = noteHealthResponse(health);
        if (skew) ports.notices().publish(skew);
        // Recovery mode exposes no project or session routes.
        if (health.status === "recovery") {
          return ports.projects()?.state.projects ?? [];
        }
      } catch {
        host.assertCurrent(generation, client);
        if (!isTauriRuntime()) throw new Error("Health check failed");
      }
    }

    const hostInfo = await host.handshake(client, generation);
    // An incompatible host is presented as a critical stop; none of its routes are used.
    if (!hostInfo.compatible) {
      ports.resetEvents(appStore);
      return ports.projects()?.state.projects ?? [];
    }
    // Another install shares nothing with cached state.
    revisionChanged ||= hostInfo.hostChanged;

    if (revisionChanged) ports.resetEvents(appStore);
    const registry = ports.projects();
    const projects = registry
      ? await registry.refresh(client)
      : await client.listProjects();
    host.assertCurrent(generation, client);
    await reconcileActiveShellProject(appStore, client);
    host.assertCurrent(generation, client);

    if (revisionChanged) {
      void persistAppStateInBackground({ lastSessionSnapshot: undefined });
      await reconcileAfterStoreRevisionChange(appStore, client);
      host.assertCurrent(generation, client);
    }
    return projects;
  }


  async function reconnect(appStore: AppStore): Promise<void> {
    const client = host.client;
    if (!client) return;
    const generation = host.generation;
    const connection = getBackendConnection() ?? host.connection;

    try {
      let revisionChanged = false;
      let recovery = false;
      if (connection) {
        const health = await fetchHealth(connection.baseUrl);
        host.assertCurrent(generation, client);
        revisionChanged = noteStoreRevision(health.store_revision);
        const skew = noteHealthResponse(health);
        if (skew) ports.notices().publish(skew);
        recovery = health.status === "recovery";
      }
      if (recovery) {
        return;
      }
      const hostInfo = await host.handshake(client, generation);
      if (!hostInfo.compatible) {
        ports.resetEvents(appStore);
        return;
      }
      if (revisionChanged || hostInfo.hostChanged) {
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
  }

  return { projectsRegistry, handleSessionGone, reconcileActiveShellProject, syncProjectsFromBackend, reconcileAfterStoreRevisionChange, reconnect };
}
