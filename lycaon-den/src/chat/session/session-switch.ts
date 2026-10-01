import type { SessionScope } from "./session-scope.ts";
import type { NoticeScope } from "../../notices/notice-scope.ts";
import { sessionScope, APP_SCOPE } from "../../notices/notice-scope.ts";
import { SESSION_CREATE_PENDING_ID } from "./session-scope.ts";
import { isSessionNotFoundError } from "./session-not-found.ts";
import { clientNoticeError } from "../../notices/client-notices.ts";
import { clearRevealHighlight } from "../transcript/presentation/reveal-highlight.ts";
import type { SwitchKind } from "../../store/shell-store.ts";

type SessionSwitchGeneration = {
  next: () => number;
  isLatest: (generation: number) => boolean;
  /** Aborted when the next generation begins. */
  signal: () => AbortSignal;
};

export function createSessionSwitchGeneration(): SessionSwitchGeneration {
  let token = 0;
  let controller = new AbortController();
  return {
    next: () => {
      controller.abort();
      controller = new AbortController();
      return ++token;
    },
    isLatest: (generation) => generation === token,
    signal: () => controller.signal,
  };
}

/** Cancels stale shell navigation work. */
export const shellSessionSwitchGeneration = createSessionSwitchGeneration();

export type SessionSwitchDeps = {
  /** Leave failed transitions on a retryable Home stage. */
  returnToHome: () => void;
  clearChatForSessionSwitch: (hydrationLock?: string) => void;
  beginSessionResumeSwitch: (hydrationLock?: string) => void;
  completeChatSessionHydration: () => void;
  resetChat: () => void;
  /** Report against the originating scope. */
  reportError: (err: unknown, scope: NoticeScope) => void;
  openSession: (scope: SessionScope) => void;
  snapshotSessionChat: () => void;
  /** Bind an LRU snapshot before the chat column remounts. */
  restoreCachedSessionChat: (scope: SessionScope) => boolean;
  setConnected?: () => void;
  prepareProject?: (projectId: string) => Promise<void>;
};

type SessionHydrateContext = {
  /** False after a newer navigation starts. */
  shouldApply: () => boolean;
  /** Aborts reads superseded by a later switch. */
  signal: AbortSignal;
};

type ResumeSessionParams = {
  generation: SessionSwitchGeneration;
  scope: SessionScope;
  kind: SwitchKind;
  deps: SessionSwitchDeps;
  hasClient: boolean;
  hydrate: (ctx: SessionHydrateContext) => Promise<void>;
  afterHydrate?: (scope: SessionScope) => Promise<void>;
  onSessionNotFound?: (scope: SessionScope) => void | Promise<void>;
};

type CreateSessionParams = {
  generation: SessionSwitchGeneration;
  projectId: string;
  deps: SessionSwitchDeps;
  hasClient: boolean;
  create: (ctx: SessionHydrateContext) => Promise<SessionScope>;
  enrich?: (scope: SessionScope, ctx: SessionHydrateContext) => Promise<void>;
  afterEnrich?: (scope: SessionScope) => void | Promise<void>;
  onOpened?: (scope: SessionScope) => void | Promise<void>;
};

type CreateSessionResult = {
  shouldApply: () => boolean;
  scope: SessionScope;
  enrichment: Promise<void>;
};

function deferSessionFollowup(
  deps: SessionSwitchDeps,
  scope: NoticeScope,
  fn: () => void | Promise<void>,
): Promise<void> {
  const followup = Promise.resolve().then(fn);
  void followup.catch((err) => deps.reportError(err, scope));
  return followup;
}

const DEFAULT_OFFLINE = clientNoticeError("offline");

function sessionSwitchError(err: unknown): unknown {
  if (err instanceof Error && err.message.trim()) return err;
  if (typeof err === "string" && err.trim()) return new Error(err.trim());
  return null;
}

/** Preserve same-project transcripts while a resumed session hydrates. */
export async function runResumeSession(params: ResumeSessionParams): Promise<void> {
  // Clear the outgoing chat's reveal marker.
  clearRevealHighlight();
  const generation = params.generation.next();
  const signal = params.generation.signal();
  if (!params.hasClient) {
    params.deps.reportError(
      DEFAULT_OFFLINE,
      sessionScope(params.scope.projectId, params.scope.sessionId),
    );
    return;
  }
  if (!params.generation.isLatest(generation)) return;

  try {
    params.deps.snapshotSessionChat();

    if (params.kind === "cross-project") {
      params.deps.clearChatForSessionSwitch("*");
      if (!params.generation.isLatest(generation)) return;
      params.deps.openSession(params.scope);
      await params.deps.prepareProject?.(params.scope.projectId);
    } else {
      // Selection moves before project preparation, as on the cross-project branch.
      params.deps.restoreCachedSessionChat(params.scope);
      params.deps.beginSessionResumeSwitch(params.scope.sessionId);
      params.deps.openSession(params.scope);
      await params.deps.prepareProject?.(params.scope.projectId);
    }

    if (!params.generation.isLatest(generation)) return;
    const shouldApply = () => params.generation.isLatest(generation);
    await params.hydrate({ shouldApply, signal });
    if (!shouldApply()) return;
    params.deps.completeChatSessionHydration();
    params.deps.setConnected?.();
    const afterHydrate = params.afterHydrate;
    if (afterHydrate) {
      const scope = sessionScope(params.scope.projectId, params.scope.sessionId);
      void deferSessionFollowup(params.deps, scope, async () => {
        if (!shouldApply()) return;
        await afterHydrate(params.scope);
      });
    }
  } catch (err) {
    if (!params.generation.isLatest(generation)) return;
    if (isSessionNotFoundError(err)) {
      try {
        await params.onSessionNotFound?.(params.scope);
      } catch {
        // Continue reset when cleanup fails.
      }
    }
    params.deps.resetChat();
    params.deps.returnToHome();
    params.deps.reportError(
      sessionSwitchError(err) ?? clientNoticeError("session_load_failed"),
      sessionScope(params.scope.projectId, params.scope.sessionId),
    );
  }
}

/** Reveal a created session before deferred enrichment. */
export async function runCreateSession(
  params: CreateSessionParams,
): Promise<CreateSessionResult | null> {
  const generation = params.generation.next();
  const signal = params.generation.signal();
  if (!params.hasClient) {
    params.deps.reportError(
      DEFAULT_OFFLINE,
      APP_SCOPE,
    );
    return null;
  }
  if (!params.generation.isLatest(generation)) return null;
  const shouldApply = () => params.generation.isLatest(generation);
  params.deps.clearChatForSessionSwitch("*");
  params.deps.openSession({
    projectId: params.projectId,
    sessionId: SESSION_CREATE_PENDING_ID,
  });
  try {
    const scope = await params.create({ shouldApply, signal });
    if (!params.generation.isLatest(generation)) {
      return null;
    }
    params.deps.openSession(scope);
    if (!params.generation.isLatest(generation)) {
      return null;
    }
    // Subscribe before revealing the chat.
    await params.deps.prepareProject?.(scope.projectId);
    if (!params.generation.isLatest(generation)) return null;
    params.deps.completeChatSessionHydration();
    params.deps.setConnected?.();
    const created = sessionScope(scope.projectId, scope.sessionId);
    const enrich = params.enrich;
    const enrichment = enrich
      ? deferSessionFollowup(params.deps, created, async () => {
          if (!shouldApply()) return;
          await enrich(scope, { shouldApply, signal });
        })
      : Promise.resolve();
    const afterEnrich = params.afterEnrich;
    if (afterEnrich) {
      void enrichment
        .then(() =>
          deferSessionFollowup(params.deps, created, async () => {
            if (!shouldApply()) return;
            await afterEnrich(scope);
          }),
        )
        .catch(() => undefined);
    }
    const onOpened = params.onOpened;
    if (onOpened) {
      void deferSessionFollowup(params.deps, created, async () => {
        if (!shouldApply()) return;
        await onOpened(scope);
      });
    }
    return { scope, shouldApply, enrichment };
  } catch (err) {
    if (!params.generation.isLatest(generation)) return null;
    params.deps.resetChat();
    params.deps.returnToHome();
    params.deps.reportError(
      sessionSwitchError(err) ?? clientNoticeError("session_create_failed"),
      APP_SCOPE,
    );
    return null;
  }
}
