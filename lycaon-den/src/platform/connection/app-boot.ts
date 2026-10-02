import { firstRunSetupCompletedPref, onboardingPrefsReady } from "../../settings/system/onboarding-prefs.ts";
import { isLoaded } from "../../store/load-state.ts";
import { watchWindowExitForHotExit } from "../../files/documents/files-hot-exit-window.ts";
import { watchWindowExitForFilesTreeView } from "../../files/tree/files-tree-view-window.ts";
import { watchWindowExitForAppState } from "../persistence/app-state-window.ts";
import { discardPendingItemWindowDrags } from "../windows/item-windows.ts";
import { resolveFilesTreeWindowLabel } from "../../files/tree/files-tree-view-state.ts";
import { resolveTranscriptViewportWindowLabel } from "../../chat/stream/transcript-viewport-state.ts";
import { installMainThreadPerfObserver } from "../../chat/stream/den-main-thread-perf.ts";
import {
  getAppStateSnapshot,
  loadSharedAppState,
  persistAppState,
} from "../../store/app-state-snapshot.ts";
import {
  applyBootAppStateSnapshot,
  subscribeAppStateBroadcast,
} from "../persistence/app-state-sync.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ShellStore } from "../../store/shell-store.ts";
import {
  bindCreatedSession,
  createSessionForProject,
  enrichCreatedSession,
  recordCreatedSessionInRecents,
  refreshCreatedSessionBoard,
  resumeChatSession,
} from "../../chat/session/session-lifecycle.ts";
import { completeChatHydrationAndReplay } from "../../chat/transcript/projection/message-events.ts";
import { workspacePreparation } from "../../shell/workspace-preparation.ts";
import {
  runCreateSession,
  runResumeSession,
  shellSessionSwitchGeneration,
  type SessionSwitchDeps,
} from "../../chat/session/session-switch.ts";
import { prepareProjectScope } from "../../chat/session/prepare-project.ts";
import { dropSessionChatCache, rememberSessionChatFromStore, restoreCachedSessionChat } from "../../chat/session/session-chat-cache.ts";
import {
  clearPersistedLastSessionSnapshot,
  parsePersistedSessionSnapshot,
  persistedSnapshotMatchesRevision,
  seedSessionChatCacheFromPersisted,
  toSessionChatSnapshot,
} from "../../chat/session/session-chat-persist.ts";
import { prefetchRecentSessionCaches } from "../../chat/session/session-prefetch.ts";
import { reconcileActiveScope } from "../../chat/session/session-reconcile.ts";
import { lastSeenStoreRevision } from "./health.ts";
import {
  attachKnownBackend,
  connectAppBackend,
  disconnectAppBackend,
  followEngineState,
  getLycaonClient,
  handleSessionGone,
  noticeReporterFor,
  subscribeProjectEvents,
} from "./app-connection.ts";
import { dismissBootFallback } from "./boot-fallback.ts";
import { readSidecarInfo } from "./backend.ts";
import { appBootPreparation } from "./app-boot-readiness.ts";

export type AppBootStores = {
  appStore: AppStore;
  projects: ProjectsStore;
  recents: RecentsStore;
  shell: ShellStore;
  /** Detached windows open the requested session. */
  requestedSession?: { projectId: string; sessionId: string } | null;
  /** Detached context windows attach to this project. */
  requestedProjectId?: string | null;
  /** Secondary windows attach to the running engine. */
  attachOnly?: boolean;
};

function bootSessionSwitchDeps(
  shell: ShellStore,
  appStore: AppStore,
  projects: ProjectsStore,
): SessionSwitchDeps {
  return {
    returnToHome: () => shell.clearToHome(),
    clearChatForSessionSwitch: (lock) =>
      appStore.actions.clearChatForSessionSwitch(lock),
    beginSessionResumeSwitch: (lock) =>
      appStore.actions.beginSessionResumeSwitch(lock),
    completeChatSessionHydration: () =>
      completeChatHydrationAndReplay(appStore),
    resetChat: () => appStore.actions.resetChatForSessionSwitch(),
    reportError: (err, scope) => noticeReporterFor(scope).reportError(err),
    openSession: (scope) =>
      shell.commitStageScope({
        projectId: scope.projectId,
        sessionId: scope.sessionId,
      }),
    setConnected: () => shell.setConnection("connected"),
    prepareProject: (projectId) =>
      prepareProjectScope(appStore, projects, projectId),
    snapshotSessionChat: () => rememberSessionChatFromStore(appStore),
    restoreCachedSessionChat: (scope) =>
      restoreCachedSessionChat(appStore, {
        projectId: scope.projectId,
        sessionId: scope.sessionId,
      }),
  };
}

/** Clear boot-painted projects absent from the hydrated registry. */
async function clearStaleBootScope(stores: AppBootStores): Promise<boolean> {
  const projectId = stores.shell.state.activeProjectId?.trim();
  if (!projectId) return false;
  if (!isLoaded(stores.projects.state.registry)) return false;
  if (stores.projects.byId(projectId)) return false;

  await abandonPersistedBootScope(stores);
  return true;
}

async function abandonPersistedBootScope(stores: AppBootStores): Promise<void> {
  await clearPersistedLastSessionSnapshot();
  await persistAppState({
    lastActiveProjectId: undefined,
    cachedProjects: undefined,
  });
  stores.appStore.actions.resetChatForSessionSwitch();
  stores.shell.clearToHome();
}

/** Paint last session from disk before the sidecar connects. */
function applyPersistedBootScope(stores: AppBootStores): boolean {
  const projectId = getAppStateSnapshot().lastActiveProjectId?.trim();
  if (!projectId) return false;
  if (!stores.projects.byId(projectId)) return false;

  const persisted = parsePersistedSessionSnapshot(
    getAppStateSnapshot().lastSessionSnapshot,
  );
  if (!persisted) return false;
  if (persisted.scope.projectId !== projectId) return false;

  stores.appStore.actions.restoreSessionChatSnapshot(
    toSessionChatSnapshot(persisted),
  );
  stores.shell.beginStageSwitch({ projectId, kind: "cross-project" });
  stores.shell.commitStageScope({
    projectId,
    sessionId: persisted.scope.sessionId,
  });
  seedSessionChatCacheFromPersisted();
  return true;
}

async function reconcileBootScope(stores: AppBootStores): Promise<void> {
  const client = getLycaonClient();
  const fg = stores.shell.state.foreground;
  const projectId = stores.shell.state.activeProjectId?.trim();
  if (!client || !fg?.projectId || !fg.sessionId || !projectId) return;

  if (!stores.projects.byId(projectId)) {
    await clearStaleBootScope(stores);
    return;
  }

  subscribeProjectEvents(stores.appStore, fg.projectId);
  stores.appStore.actions.beginSessionResumeSwitch(fg.sessionId);
  try {
    const session = stores.appStore.state.currentSession;
    const sessionId = session?.id?.trim();
    const projectDir = session?.workspace_path?.trim();
    if (!sessionId || !projectDir || sessionId !== fg.sessionId) {
      throw new Error("boot reconcile: incomplete persisted session");
    }
    await reconcileActiveScope(
      stores.appStore,
      client,
      stores.projects.state.projects,
      { resumeVisible: true },
    );
  } catch {
    await abandonPersistedBootScope(stores);
  }
}

async function restoreLastActiveProject(stores: AppBootStores): Promise<void> {
  const projectId = getAppStateSnapshot().lastActiveProjectId?.trim();
  if (!projectId) return;

  const client = getLycaonClient();
  if (!client) return;

  if (!stores.projects.byId(projectId)) {
    await persistAppState({ lastActiveProjectId: undefined });
    stores.shell.clearToHome();
    return;
  }

  stores.shell.beginStageSwitch({ projectId, kind: "cross-project" });

  const recent = stores.recents.state.recents.find(
    (row) => row.projectId === projectId,
  );
  const deps = bootSessionSwitchDeps(stores.shell, stores.appStore, stores.projects);

  if (recent) {
    const scope = { projectId: recent.projectId, sessionId: recent.sessionId };
    await runResumeSession({
      generation: shellSessionSwitchGeneration,
      scope,
      kind: "cross-project",
      deps,
      hasClient: true,
      hydrate: async ({ shouldApply }) => {
        await resumeChatSession(
          stores.appStore,
          client,
          scope.sessionId,
          stores.projects.state.projects,
          stores.recents,
          { projectId: scope.projectId, shouldApply },
        );
      },
    });
    return;
  }

  await runCreateSession({
    generation: shellSessionSwitchGeneration,
    projectId,
    deps,
    hasClient: true,
    create: async ({ shouldApply }) => {
      const session = await createSessionForProject(client, projectId);
      if (!shouldApply()) {
        return {
          projectId: session.project_id?.trim() || projectId,
          sessionId: session.id,
        };
      }
      bindCreatedSession(stores.appStore, session);
      return {
        projectId: session.project_id?.trim() || projectId,
        sessionId: session.id,
      };
    },
    enrich: async (scope, { shouldApply }) => {
      await workspacePreparation(scope.projectId).run(
        "session-enrichment",
        () => enrichCreatedSession(
          stores.appStore,
          client,
          scope.sessionId,
          stores.projects.state.projects,
          { projectId: scope.projectId, shouldApply },
          stores.recents,
        ),
      );
    },
    afterEnrich: (scope) => {
      const projectDir = stores.appStore.state.currentSession?.workspace_path;
      if (projectDir) {
        // The board and its git read land with the workspace.
        void workspacePreparation(scope.projectId).run(
          "session-board",
          () => refreshCreatedSessionBoard(stores.appStore, client, stores.projects, projectDir, scope.sessionId),
        );
      }
    },
    onOpened: (scope) => recordCreatedSessionInRecents(stores.recents, scope),
  });
}

async function restoreRequestedSession(
  stores: AppBootStores,
  scope: { projectId: string; sessionId: string },
): Promise<void> {
  const client = getLycaonClient();
  if (!client || !stores.projects.byId(scope.projectId)) return;
  const deps = bootSessionSwitchDeps(stores.shell, stores.appStore, stores.projects);
  await runResumeSession({
    generation: shellSessionSwitchGeneration,
    scope,
    kind: "cross-project",
    deps,
    hasClient: true,
    hydrate: async ({ shouldApply }) => {
      await resumeChatSession(
        stores.appStore,
        client,
        scope.sessionId,
        stores.projects.state.projects,
        stores.recents,
        { projectId: scope.projectId, shouldApply },
      );
    },
  });
}

async function restoreRequestedProject(
  stores: AppBootStores,
  projectId: string,
): Promise<void> {
  if (!getLycaonClient() || !stores.projects.byId(projectId)) return;
  stores.shell.beginStageSwitch({ projectId, kind: "cross-project" });
  stores.shell.setConnection("connected");
  subscribeProjectEvents(stores.appStore, projectId);
}

async function discardStaleBootSnapshot(stores: AppBootStores): Promise<void> {
  const scope = stores.shell.state.foreground;
  const projectId = stores.shell.state.activeProjectId;
  await clearPersistedLastSessionSnapshot();
  if (scope) dropSessionChatCache(scope);
  stores.appStore.actions.resetChatForSessionSwitch();
  // The cached transcript expires with the host revision; the navigation target does not.
  if (projectId) stores.shell.beginStageSwitch({ projectId, kind: "cross-project" });
  else stores.shell.clearToHome();
}

async function bootstrapAppBackend(
  stores: AppBootStores,
  bootScopeApplied: boolean,
): Promise<void> {
  stores.appStore.actions.setSidecarStatus("connecting");
  disconnectAppBackend();

  let connected = false;
  try {
    if (stores.attachOnly) {
      const connection = await readSidecarInfo();
      if (connection) {
        attachKnownBackend(stores.appStore, connection);
        connected = true;
      }
    } else {
      await connectAppBackend(stores.appStore);
      connected = true;
    }
  } catch {
    /* offline — shell still mounts from persisted state */
  }

  let scopeFromDisk = bootScopeApplied;
  const revision = lastSeenStoreRevision();
  if (scopeFromDisk && !persistedSnapshotMatchesRevision(revision)) {
    await discardStaleBootSnapshot(stores);
    scopeFromDisk = false;
  }

  try {
    // Item windows hydrate the shared project registry.
    if (stores.attachOnly || !connected) {
      await stores.projects.hydrate();
    }
  } catch {
    /* offline boot still needs loaded=true without a second listProjects race */
  }

  if (await clearStaleBootScope(stores)) {
    return;
  }

  const client = getLycaonClient();
  if (!client || !connected) {
    if (scopeFromDisk) {
      await abandonPersistedBootScope(stores);
    }
    return;
  }

  try {
    if (stores.requestedSession) {
      await restoreRequestedSession(stores, stores.requestedSession);
    } else if (stores.requestedProjectId) {
      await restoreRequestedProject(stores, stores.requestedProjectId);
    } else if (scopeFromDisk) {
      await reconcileBootScope(stores);
    } else {
      await restoreLastActiveProject(stores);
    }
  } catch {
    if (scopeFromDisk) {
      await abandonPersistedBootScope(stores);
    }
  }

  void prefetchRecentSessionCaches(
    client,
    stores.recents.state.recents,
    stores.projects.state.projects,
    handleSessionGone,
  ).catch(() => undefined);
}

/** Load persisted state, reveal the shell, then attach the backend in the background. */
export async function startAppBoot(stores: AppBootStores): Promise<void> {
  const releaseBoot = appBootPreparation.register("backend", () => false);
  try {
    await loadAppBoot(stores, releaseBoot);
  } catch (error) {
    releaseBoot();
    throw error;
  }
}

async function loadAppBoot(stores: AppBootStores, releaseBoot: () => void): Promise<void> {
  // Keep the connection state stable during bootstrap.
  stores.appStore.actions.setSidecarStatus("connecting");
  // Install preservation before loading state, so quit also works during boot
  // or when a persisted slice cannot be read.
  watchWindowExitForHotExit();
  watchWindowExitForFilesTreeView();
  watchWindowExitForAppState();
  let bootScopeApplied = false;
  try {
    await loadSharedAppState();
    await Promise.all([
      resolveFilesTreeWindowLabel(),
      resolveTranscriptViewportWindowLabel(),
    ]);
    applyBootAppStateSnapshot();
    const cached = getAppStateSnapshot().cachedProjects;
    if (cached?.length) {
      stores.projects.seedFromSnapshot(cached);
    }
    // Restored debug preferences determine whether the observer runs.
    installMainThreadPerfObserver();
    subscribeAppStateBroadcast();
    // Reloading ends pending drag gestures.
    void discardPendingItemWindowDrags();
    await stores.recents.load();
    seedSessionChatCacheFromPersisted();
    // Addressed item windows skip the default session restore.
    bootScopeApplied =
      stores.requestedSession || stores.requestedProjectId
        ? false
        : applyPersistedBootScope(stores);
  } finally {
    // Unlatched first-run keeps splash until Shell mounts OnboardingGate (or goes offline).
    const holdSplashForFirstRun =
      onboardingPrefsReady() && !firstRunSetupCompletedPref();
    if (stores.attachOnly) {
      // Attached views skip the brand reveal.
      await dismissBootFallback({ immediate: true });
    } else if (!holdSplashForFirstRun) {
      await dismissBootFallback();
    }
  }

  // Listen first, so the engine this boot connects to is the one followed.
  await followEngineState(stores.appStore);
  void bootstrapAppBackend(stores, bootScopeApplied).finally(releaseBoot);
}
