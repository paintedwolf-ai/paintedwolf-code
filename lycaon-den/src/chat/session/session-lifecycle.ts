import type { LycaonClient } from "../../api/client.ts";
import { beginSessionSnapshotRead, refreshSessionSnapshot } from "../../api/session-snapshot-refresh.ts";
import { appliedSessionRevision } from "../../api/session-event-revisions.ts";
import type { Message, Project, Session } from "../../api/types.ts";
import { isEnterableSessionStatus } from "../../api/session-status.generated.ts";
import {
  noticeReporterFor,
  onSessionEvent,
  resumeProjectEventsAfter,
} from "../../platform/connection/app-connection.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { DEFAULT_SESSION_TITLE } from "../../../shared/app-state-types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import {
  getSessionChatCache,
  rememberSessionChatFromStore,
  dropSessionChatCache,
} from "./session-chat-cache.ts";
import type { SessionChatScope } from "./session-chat-snapshot.ts";
import { reconcileActiveScope } from "./session-reconcile.ts";
import { isExitSlashCommand } from "../composer/composer-rules.ts";
import { refreshBoard, refreshSessionWorkers } from "../actions/board-actions.ts";
import {
  exitActiveWorkflow,
  refreshWorkflowState,
} from "../workflow/workflow-actions.ts";
import { applySessionTranscriptSnapshot } from "./session-transcript-hydrate.ts";
import { applySessionBootstrapChrome } from "./session-chrome.ts";
import { cancelPromptSubmissions, reservePromptSubmission } from "../send/prompt-submission-order.ts";
import { pendingPromptKind } from "../send/pending-sends.ts";

/** Large snapshots yield a frame before hydration. */
const LARGE_TRANSCRIPT_CHARS = 96_000;

function transcriptCharEstimate(messages: readonly Message[]): number {
  let n = 0;
  for (const m of messages) {
    n += m.content.length;
  }
  return n;
}

async function yieldUiThreadIfNeeded(messages: readonly Message[]): Promise<void> {
  if (transcriptCharEstimate(messages) < LARGE_TRANSCRIPT_CHARS) return;
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => resolve());
  });
}

async function refreshSessionStatusAfterPrompt(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
): Promise<void> {
  try {
    await refreshSessionSnapshot(appStore, client, sessionId);
  } catch {
    /* Status refresh is optional. */
  }
}

/** Recents rows update in memory at once; their disk write never gates the chat. */
function registerRecentTitle(recents: RecentsStore, session: Session): void {
  const title = session.title?.trim();
  if (!title) return;
  void recents
    .registerSession({ projectId: session.project_id, sessionId: session.id, title })
    .catch(() => undefined);
}

export async function recordCreatedSessionInRecents(
  recents: RecentsStore,
  scope: { projectId: string; sessionId: string },
): Promise<void> {
  await recents.recordSessionActivity({
    projectId: scope.projectId,
    sessionId: scope.sessionId,
    title: DEFAULT_SESSION_TITLE,
  });
}

export async function createSessionForProject(
  client: LycaonClient,
  projectId: string,
): Promise<Session> {
  return client.createSession({
    posture: "build",
    project_id: projectId,
  });
}

export function bindCreatedSession(appStore: AppStore, session: Session): void {
  appStore.actions.setCurrentSession(session);
  appStore.actions.installTranscriptBaseline(session.id, [], 0);
}

/** Keep live prompt state when applying a bootstrap snapshot. */
export function mergeCreatedSessionBootstrap(
  live: Session,
  bootstrap: Session,
): Session {
  if (live.id.trim() !== bootstrap.id.trim()) return bootstrap;
  return {
    ...bootstrap,
    status: isEnterableSessionStatus(bootstrap.status)
      ? bootstrap.status
      : live.status,
    title: bootstrap.title?.trim() ? bootstrap.title : live.title,
    ui: bootstrap.ui ?? live.ui,
  };
}

type EnrichCreatedSessionOpts = {
  projectId: string;
  shouldApply?: () => boolean;
};

export async function enrichCreatedSession(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projects: readonly Project[],
  opts: EnrichCreatedSessionOpts,
  recents?: RecentsStore,
): Promise<void> {
  const sid = sessionId.trim();
  if (!sid) return;
  const projectId = opts.projectId.trim();
  const scope: SessionChatScope | null =
    projectId && sid ? { projectId, sessionId: sid } : null;
  const shouldApply = opts.shouldApply ?? (() => true);

  // Start created sessions from an empty cache.
  if (scope) dropSessionChatCache(scope);

  const bootstrap = await client.getSessionBootstrap(sid);
  if (!shouldApply()) return;

  const live = appStore.state.currentSession;
  const session = bootstrap.session;
  let installedSession = session;
  if (live?.id?.trim() === session.id.trim()) {
    installedSession = mergeCreatedSessionBootstrap(live, session);
  }
  applySessionBootstrapChrome(appStore, bootstrap, installedSession);
  if (recents) registerRecentTitle(recents, session);

  const { transcript } = bootstrap;
  const projectDir = session.workspace_path?.trim() ?? "";
  await yieldUiThreadIfNeeded(transcript.messages);
  if (!shouldApply()) return;
  await applySessionTranscriptSnapshot(
    appStore,
    client,
    sid,
    projectDir,
    transcript,
    projects,
  );
  if (!shouldApply()) return;
  if (scope) rememberSessionChatFromStore(appStore, scope);
}

const DEFAULT_PROMPTABLE_TIMEOUT_MS = 60_000;
const PROMPTABLE_POLL_MS = 250;

type WaitForSessionPromptableOpts = {
  /** Cancel when navigation replaces this session. */
  shouldApply?: () => boolean;
  timeoutMs?: number;
};

export async function waitForSessionPromptable(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  opts?: WaitForSessionPromptableOpts,
): Promise<void> {
  const sid = sessionId.trim();
  if (!sid) {
    throw new Error("Session is not ready.");
  }
  const shouldApply = opts?.shouldApply ?? (() => true);
  const timeoutMs = opts?.timeoutMs ?? DEFAULT_PROMPTABLE_TIMEOUT_MS;

  const phase = (): "missing" | "preparing" | "error" | "ready" => {
    const session = appStore.state.currentSession;
    if (!session || session.id.trim() !== sid) return "missing";
    if (session.status === "preparing") return "preparing";
    if (session.status === "error") return "error";
    return "ready";
  };

  if (phase() === "ready") return;
  if (phase() === "error") {
    throw new Error("Session failed to open.");
  }

  await new Promise<void>((resolve, reject) => {
    const started = Date.now();
    let settled = false;
    const finish = (err?: Error) => {
      if (settled) return;
      settled = true;
      globalThis.clearInterval(poll);
      unsub();
      if (err) reject(err);
      else resolve();
    };
    const check = () => {
      if (!shouldApply()) {
        finish(new Error("Chat was replaced before it finished opening."));
        return;
      }
      const state = phase();
      if (state === "ready") {
        finish();
        return;
      }
      if (state === "error") {
        finish(new Error("Session failed to open."));
        return;
      }
      if (Date.now() - started >= timeoutMs) {
        finish(new Error("Timed out waiting for chat to open."));
      }
    };
    const unsub = onSessionEvent((ev) => {
      if (ev.id.trim() === sid) check();
    });
    let refreshInFlight = false;
    const poll = globalThis.setInterval(() => {
      check();
      if (settled || refreshInFlight) return;
      refreshInFlight = true;
      void refreshSessionSnapshot(appStore, client, sid, () => !settled && shouldApply())
        .then(check)
        .catch(check)
        .finally(() => { refreshInFlight = false; });
    }, PROMPTABLE_POLL_MS);
    check();
  });
}

export async function refreshCreatedSessionBoard(
  appStore: AppStore,
  client: LycaonClient,
  projects: ProjectsStore,
  projectDir: string,
  sessionId: string,
): Promise<void> {
	const epoch = appStore.state.sessionViewEpoch;
  try {
    await projects.refresh(client);
  } catch {
    /* Project refresh is optional. */
  }
  const list = projects.state.projects;
  if (appStore.state.sessionViewEpoch !== epoch || appStore.state.currentSession?.id !== sessionId) return;
  await refreshBoard(appStore, client, projectDir, list, sessionId, {
    includeGit: true,
  }).catch(() => undefined);
}

type ResumeChatSessionOpts = {
  projectId?: string;
  /** Drop store writes when a newer navigation superseded this hydrate. */
  shouldApply?: () => boolean;
  /** Aborts the bootstrap read when a newer navigation supersedes this one. */
  signal?: AbortSignal;
};

/** Hydration leaves foreground navigation to the caller. */
export async function resumeChatSession(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projects: readonly Project[],
  recents?: RecentsStore,
  opts?: ResumeChatSessionOpts,
): Promise<void> {
  const sid = sessionId.trim();
  const projectId =
    opts?.projectId?.trim() ?? appStore.state.currentSession?.project_id?.trim();
  const scope: SessionChatScope | null =
    projectId && sid ? { projectId, sessionId: sid } : null;
  const shouldApply = opts?.shouldApply ?? (() => true);
  const cached = scope ? getSessionChatCache(scope) : undefined;

  if (cached) {
    if (!shouldApply()) return;
    appStore.actions.restoreSessionChatSnapshot(cached);
    // Cached data retains the requested session identity.
    if (appStore.state.currentSession?.id?.trim() === sid) {
      if (recents) registerRecentTitle(recents, cached.session);
      await reconcileActiveScope(appStore, client, projects, {
        resumeVisible: true,
        fromSessionSwitch: true,
        shouldApply,
        signal: opts?.signal,
      });
      if (!shouldApply()) return;
      if (scope) rememberSessionChatFromStore(appStore, scope);
      return;
    }
    if (scope) dropSessionChatCache(scope);
  }

  const bootstrap = await client.getSessionBootstrap(sid, { signal: opts?.signal });
  const { session, transcript } = bootstrap;
  if (!shouldApply()) return;
  const projectDir = session.workspace_path?.trim() ?? "";
  applySessionBootstrapChrome(appStore, bootstrap);
  resumeProjectEventsAfter(bootstrap.event_cursor);
  // Registration preserves recent ordering.
  if (recents) registerRecentTitle(recents, session);
  await yieldUiThreadIfNeeded(transcript.messages);
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
  if (scope) rememberSessionChatFromStore(appStore, scope);
}

type SendChatPromptOpts = {
  recovery?: import("../../api/types.ts").PromptRecovery;
  /** Fires after the pending submission is visible, before any network wait. */
  onPendingSend?: (destination: "transcript" | "queue") => void;
  attachmentLabels?: readonly string[];
  shouldSend?: (client: LycaonClient) => boolean;
};

/** Resolves the client once pre-send work is done; runs after pending display. */
export type PrepareChatSend = () => Promise<LycaonClient>;

export async function sendChatPrompt(
  appStore: AppStore,
  recents: RecentsStore,
  prepareSend: PrepareChatSend,
  sessionId: string,
  projectId: string,
  projectDir: string,
  projects: readonly Project[],
  text: string,
  attachments?: import("../../api/types.ts").PromptAttachmentPart[],
  references?: import("../../api/types.ts").PromptReferencePart[],
  secrets?: import("../../api/types.ts").PromptSecretReferencePart[],
  opts?: SendChatPromptOpts,
): Promise<void> {
  const trimmed = text.trim();
  const attachmentParts = attachments?.filter((a) => a.blob_id?.trim()) ?? [];
  const referenceParts =
    references?.filter((r) => {
      if (!r.kind || !r.project_id?.trim()) return false;
      if (r.kind === "path-file" || r.kind === "path-folder") {
        return Boolean(r.root_id?.trim() && r.path?.trim());
      }
      if (r.kind === "artifact") return Boolean(r.artifact_id?.trim());
      if (r.kind === "search-hit") {
        return Boolean(r.session_id?.trim() && r.source_ref?.trim() && r.hit_kind?.trim());
      }
      return false;
    }) ?? [];
  const secretParts = secrets?.filter((part) => part.reference?.trim()) ?? [];
  const reporter = noticeReporterFor(sessionScope(projectId, sessionId));
  let backendRead: ReturnType<typeof beginSessionSnapshotRead> | undefined;
  const isSameBackend = () => backendRead?.isSameBackend() ?? false;
  const requirePreparedClient = (client: LycaonClient) => {
    if (opts?.shouldSend?.(client) === false) {
      throw new DOMException("Chat connection changed.", "AbortError");
    }
  };
  if (trimmed && isExitSlashCommand(trimmed)) {
    let exitClient: LycaonClient;
    try {
      exitClient = await prepareSend();
      requirePreparedClient(exitClient);
      backendRead = beginSessionSnapshotRead(appStore, sessionId);
      await exitActiveWorkflow(appStore, exitClient, sessionId, projectDir, projects);
    } catch (err) {
      if ((!backendRead || isSameBackend()) && !(err instanceof DOMException && err.name === "AbortError")) reporter.reportError(err);
      throw err;
    } finally {
      backendRead?.finish();
    }
    return;
  }

  // The host echoes the operation id as the user message id.
  const operationId = crypto.randomUUID();
  const kind = pendingPromptKind(appStore, sessionId);
  appStore.actions.addPendingSend(sessionId, {
    kind,
    operationId,
    text: trimmed,
    attachmentLabels:
      opts?.attachmentLabels && opts.attachmentLabels.length > 0
        ? opts.attachmentLabels
        : undefined,
    state: "sending",
  });
  const submission = reservePromptSubmission(appStore, sessionId);
  const requirePendingSubmission = () => {
    if (submission.canceled() || !appStore.state.pendingSends[sessionId]?.some(
      (entry) => entry.operationId === operationId,
    )) throw new DOMException("Prompt was canceled before submission.", "AbortError");
  };

  let client: LycaonClient | undefined;
  try {
    opts?.onPendingSend?.(kind === "queued_prompt" ? "queue" : "transcript");
    await submission.ready;
    requirePendingSubmission();
    client = await prepareSend();
    requirePreparedClient(client);
    backendRead = beginSessionSnapshotRead(appStore, sessionId);
    await waitForSessionPromptable(appStore, client, sessionId, {
      shouldApply: () => isSameBackend() && opts?.shouldSend?.(client!) !== false,
    });
    requirePreparedClient(client);
    requirePendingSubmission();
    if (!isSameBackend()) throw new DOMException("Backend connection changed.", "AbortError");
    appStore.actions.holdPromptSubmission(sessionId, operationId);

    // Recents persistence does not block prompts.
    void recents.recordSessionActivity({ projectId, sessionId }).catch(() => undefined);

    const accepted = await client.sendPrompt(sessionId, {
      operation_id: operationId,
      recovery: opts?.recovery,
      text: trimmed,
      attachments: attachmentParts.length > 0 ? attachmentParts : undefined,
      references: referenceParts.length > 0 ? referenceParts : undefined,
      secrets: secretParts.length > 0 ? secretParts : undefined,
    });
    if (!isSameBackend()) {
      appStore.actions.removePendingSends(sessionId, [operationId]);
      return;
    }
    // A replayed terminal submission remains a failure.
    if (
      accepted.status === "failed" ||
      accepted.status === "interrupted" ||
      accepted.status === "canceled"
    ) {
      const retryHint: Record<typeof accepted.status, string> = {
        failed: "This prompt already ran and failed. Send it again to retry.",
        interrupted:
          "This prompt already ran and was interrupted. Send it again to retry.",
        canceled: "This prompt was canceled. Send it again to retry.",
      };
      throw new Error(retryHint[accepted.status]);
    }
    // Completed receipts require no admission hold.
    if (accepted.status === "complete") {
      appStore.actions.releasePromptSubmission(sessionId, operationId);
    } else {
      appStore.actions.admitPromptSubmission(
        sessionId,
        operationId,
        accepted.session_revision,
        appliedSessionRevision(appStore.actions, sessionId),
      );
    }
    // The echo may already have removed this entry.
    appStore.actions.patchPendingSend(sessionId, operationId, {
      state: "accepted",
    });
    void refreshWorkflowState(appStore, client, sessionId, projectDir, projects).catch(
      () => undefined,
    );
  } catch (err) {
    appStore.actions.removePendingSends(sessionId, [operationId]);
    if (isSameBackend()) appStore.actions.releasePromptSubmission(sessionId, operationId);
    if ((!backendRead || isSameBackend()) && !(err instanceof DOMException && err.name === "AbortError")) {
      reporter.reportError(err);
    }
    throw err;
  } finally {
    if (isSameBackend()) {
      const remember = () => {
        if (isSameBackend()) rememberSessionChatFromStore(appStore, { projectId, sessionId });
      };
      if (client) void refreshSessionStatusAfterPrompt(appStore, client, sessionId).then(remember);
      else remember();
    }
    backendRead?.finish();
    submission.finish();
  }
}

/** Stop all work in the session tree. */
export async function stopChatActivity(
  appStore: AppStore,
  client: LycaonClient | null,
  sessionId: string,
  projectId: string,
  projectDir: string,
  projects: readonly Project[],
): Promise<void> {
  if (appStore.state.sessionActivity[sessionId]?.stopping) return;
  if (!client) {
    noticeReporterFor(sessionScope(projectId, sessionId)).reportError(
      new Error("Cannot stop this chat while the app is disconnected."),
    );
    return;
  }
  appStore.actions.setSessionStopping(sessionId, true);
  cancelPromptSubmissions(appStore, sessionId);
  const read = beginSessionSnapshotRead(appStore, sessionId);
  try {
    const session = await client.abortSession(sessionId);
    if (!read.isSameBackend()) return;
    const activityUnchanged = read.isUnchanged();
    if (read.isCurrent()) appStore.actions.mergeSession(session);
    if (activityUnchanged) {
      appStore.actions.clearSessionActivity(sessionId);
    } else {
      appStore.actions.setSessionStopping(sessionId, false);
    }
    await Promise.allSettled([
      refreshSessionWorkers(appStore, client, projectDir, projects, sessionId),
      refreshWorkflowState(appStore, client, sessionId, projectDir, projects),
    ]);
  } catch (err) {
    if (read.isSameBackend()) {
      appStore.actions.setSessionStopping(sessionId, false);
      noticeReporterFor(sessionScope(projectId, sessionId)).reportError(err);
    }
  } finally {
    read.finish();
  }
}
