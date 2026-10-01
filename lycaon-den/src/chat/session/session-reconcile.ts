import type { RecentSession } from "../../../shared/app-state-types.ts";
import type { Project } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { refreshBoard } from "../actions/board-actions.ts";
import { applySessionTranscriptSnapshot } from "./session-transcript-hydrate.ts";
import { isScanNotFoundError, isSessionNotFoundError } from "./session-not-found.ts";
import type { SessionScope } from "./session-scope.ts";
import { completeChatHydrationAndReplay } from "../transcript/projection/message-events.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { applySessionBootstrapChrome } from "./session-chrome.ts";
import { resumeProjectEventsAfter } from "../../platform/connection/app-connection.ts";

export type RecentsReconcileResult = {
  kept: RecentSession[];
  gone: SessionScope[];
};

export type RecentsStoreReconcile = {
  state: { loaded: boolean; recents: RecentSession[] };
  replaceRecents(recents: RecentSession[]): Promise<void>;
};

export type ShellStoreEvict = {
  evictConversation(projectId: string, sessionId: string): void;
};

/** Reconcile local recents with durable sessions. */
export async function reconcileRecents(
  client: LycaonClient,
  recents: readonly RecentSession[],
): Promise<RecentsReconcileResult> {
  if (recents.length === 0) {
    return { kept: [], gone: [] };
  }

  const outcomes = await Promise.all(
    recents.map(async (row) => {
      try {
        await client.getSession(row.sessionId);
        return { row, gone: false as const };
      } catch (err) {
        if (isSessionNotFoundError(err)) {
          return {
            row,
            gone: true as const,
            scope: { projectId: row.projectId, sessionId: row.sessionId },
          };
        }
        return { row, gone: false as const };
      }
    }),
  );

  const kept: RecentSession[] = [];
  const gone: SessionScope[] = [];
  for (const outcome of outcomes) {
    if (outcome.gone) {
      gone.push(outcome.scope);
    } else {
      kept.push(outcome.row);
    }
  }
  return { kept, gone };
}

/** Remove recents for missing sessions. */
export async function reconcileRecentsWithStores(
  client: LycaonClient,
  recentsStore: RecentsStoreReconcile,
  shellStore: ShellStoreEvict | undefined,
  shouldApply: () => boolean,
): Promise<SessionScope[]> {
  if (!recentsStore.state.loaded || !shouldApply()) return [];
  const rows = recentsStore.state.recents;
  const { kept, gone } = await reconcileRecents(client, rows);
  if (!shouldApply() || recentsStore.state.recents !== rows || gone.length === 0) return [];
  await recentsStore.replaceRecents(kept);
  if (!shouldApply()) return [];
  for (const scope of gone) {
    shellStore?.evictConversation(scope.projectId, scope.sessionId);
  }
  return gone;
}

export type ReconcileActiveScopeOptions = {
  onSessionGone?: (scope: SessionScope) => void | Promise<void>;
  /** Keep the cached transcript visible during refresh. */
  resumeVisible?: boolean;
  /** Reject writes from superseded navigation. */
  shouldApply?: () => boolean;
  /** Marks hydration started by the active session switch. */
  fromSessionSwitch?: boolean;
  /** Resume event delivery from this bootstrap. */
  resumeEvents?: boolean;
  /** Aborts the bootstrap read when superseded. */
  signal?: AbortSignal;
};

function stillForegroundSession(appStore: AppStore, sessionId: string): boolean {
  return appStore.state.currentSession?.id?.trim() === sessionId.trim();
}

/** Reports whether the hydration lock admits this session. */
function hydrationLockAllows(appStore: AppStore, sessionId: string): boolean {
  const lock = appStore.state.chatHydrationLock?.trim();
  return !lock || lock === sessionId;
}

const bootstrapRequests = new WeakMap<AppStore, object>();

/** Reconcile active-session state from one bootstrap. */
export async function reconcileActiveScope(
  appStore: AppStore,
  client: LycaonClient,
  projects: readonly Project[],
  opts?: ReconcileActiveScopeOptions,
): Promise<string | undefined> {
  const session = appStore.state.currentSession;
  const sessionId = session?.id?.trim();
  const projectDir = session?.workspace_path?.trim();
  const projectId = session?.project_id?.trim();
  if (!sessionId || !projectDir) return;

  let epoch = appStore.state.sessionViewEpoch;
  const matchesView = () =>
    appStore.state.sessionViewEpoch === epoch &&
    (opts?.shouldApply?.() ?? stillForegroundSession(appStore, sessionId));

  if (!matchesView()) return;

  const resumeVisible = opts?.resumeVisible === true;
  if (resumeVisible) {
    const lock = appStore.state.chatHydrationLock?.trim();
    // A wildcard lock belongs to a cross-project switch.
    if (lock === "*" && !opts?.fromSessionSwitch) {
      return;
    }
    // A session lock belongs to its navigation.
    if (lock && lock !== "*" && lock !== sessionId) {
      return;
    }
    if (lock !== sessionId) {
      appStore.actions.beginSessionResumeSwitch(sessionId);
      epoch = appStore.state.sessionViewEpoch;
    }
  }

  const request = {};
  bootstrapRequests.set(appStore, request);
  const shouldApply = () => matchesView() && bootstrapRequests.get(appStore) === request;
  try {
    const bootstrap = await client.getSessionBootstrap(sessionId, { signal: opts?.signal });
    const { transcript } = bootstrap;
    if (!shouldApply()) return;
    applySessionBootstrapChrome(appStore, bootstrap);
    epoch = appStore.state.sessionViewEpoch;
    if (opts?.resumeEvents !== false) {
      resumeProjectEventsAfter(bootstrap.event_cursor);
    }
    if (!shouldApply()) return;
    await applySessionTranscriptSnapshot(
      appStore,
      client,
      sessionId,
      projectDir,
      transcript,
      projects,
    );
    if (!shouldApply()) return;
    if (resumeVisible && hydrationLockAllows(appStore, sessionId)) {
      completeChatHydrationAndReplay(appStore);
    }
    void refreshBoard(appStore, client, projectDir, projects, sessionId, {
      includeGit: true,
    }).catch(() => undefined);
    void refreshCodeScanCache(appStore, client);
    return bootstrap.event_cursor;
  } catch (err) {
    if (!shouldApply()) return;
    if (isSessionNotFoundError(err)) {
      if (projectId) {
        await opts?.onSessionGone?.({ projectId, sessionId });
      }
      if (!shouldApply()) return;
      appStore.actions.resetChatForSessionSwitch();
    } else if (resumeVisible && hydrationLockAllows(appStore, sessionId)) {
      appStore.actions.completeChatSessionHydration();
    }
    throw err;
  }
}

/** Clear cached scans whose backing row is missing. */
export async function refreshCodeScanCache(
  appStore: AppStore,
  client: LycaonClient,
): Promise<void> {
  const scanId = appStore.state.latestCodeScan?.scan_id?.trim();
  const projectId = appStore.state.currentSession?.project_id?.trim();
  if (!scanId || !projectId) return;
  try {
    await client.getCodeScan(projectId, scanId, "summary");
  } catch (err) {
    if (isScanNotFoundError(err) && appStore.state.currentSession?.project_id?.trim() === projectId && appStore.state.latestCodeScan?.scan_id?.trim() === scanId) {
      appStore.actions.clearLatestCodeScan();
    }
  }
}
