import { createSignal } from "solid-js";
import { invalidateContributionFrame } from "../contributions/contribution-store.ts";
import type { NoticeAction as NoticeActionWire } from "../api/types.ts";
import type { NoticeScope } from "./notice-scope.ts";

/** Catalog-declared actions carried on the wire. */
export type NoticeActionKind = NoticeActionWire;

export type NoticeAction = {
  id: NoticeActionKind;
  label: string;
  variant?: "primary" | "secondary";
  run: () => void;
};

/** Shell-level navigation sinks; they act on no particular chat. */
export type ShellNoticeActionSinks = {
  openAIProviders?: () => void;
};

/** Prompt recovery handlers registered by one mounted chat. */
export type SessionPromptActions = {
  retry: () => void;
  keepGoing: () => void;
  rewindAndRetry: () => void;
};

const [shellSinks, setShellSinks] = createSignal<ShellNoticeActionSinks | null>(null);

/** Each session keeps a stack so an overlapping remount never drops the surviving registration. */
const [sessionActions, setSessionActions] = createSignal<
  ReadonlyMap<string, readonly SessionPromptActions[]>
>(new Map());

export function setShellNoticeActionSinks(next: ShellNoticeActionSinks | null): void {
  setShellSinks(next);
}

/** Registers one chat's prompt recovery handlers under its own session id. */
export function registerSessionPromptActions(
  sessionId: string,
  handlers: SessionPromptActions,
): () => void {
  setSessionActions((prev) => {
    const next = new Map(prev);
    next.set(sessionId, [...(prev.get(sessionId) ?? []), handlers]);
    return next;
  });
  return () => {
    setSessionActions((prev) => {
      const stack = prev.get(sessionId);
      if (!stack?.includes(handlers)) return prev;
      const next = new Map(prev);
      const rest = stack.filter((h) => h !== handlers);
      if (rest.length > 0) next.set(sessionId, rest);
      else next.delete(sessionId);
      return next;
    });
  };
}

export function resetNoticeActionSinksForTest(): void {
  setShellSinks(null);
  setSessionActions(new Map());
}

function promptActionsFor(scope: NoticeScope | undefined): SessionPromptActions | null {
  if (scope?.kind !== "session") return null;
  const stack = sessionActions().get(scope.sessionId);
  return stack?.[stack.length - 1] ?? null;
}

function isNoticeActionKind(value: string): value is NoticeActionKind {
  return (
    value === "open_ai_providers" ||
    value === "retry_contribution_frame" ||
    value === "prompt_retry" ||
    value === "prompt_keep_going" ||
    value === "prompt_rewind_and_retry"
  );
}

function actionFor(
  kind: NoticeActionKind,
  scope: NoticeScope | undefined,
): NoticeAction | null {
  switch (kind) {
    case "open_ai_providers": {
      const open = shellSinks()?.openAIProviders;
      return open
        ? { id: kind, label: "Open AI providers", variant: "secondary", run: open }
        : null;
    }
    case "retry_contribution_frame":
      return {
        id: kind,
        label: "Retry",
        variant: "primary",
        run: () => void invalidateContributionFrame(),
      };
    case "prompt_retry": {
      const chat = promptActionsFor(scope);
      return chat ? { id: kind, label: "Retry", variant: "primary", run: chat.retry } : null;
    }
    case "prompt_keep_going": {
      const chat = promptActionsFor(scope);
      return chat
        ? { id: kind, label: "Keep going", variant: "primary", run: chat.keepGoing }
        : null;
    }
    case "prompt_rewind_and_retry": {
      const chat = promptActionsFor(scope);
      return chat
        ? {
            id: kind,
            label: "Rewind and retry",
            variant: "secondary",
            run: chat.rewindAndRetry,
          }
        : null;
    }
  }
}

type ActionableNotice = {
  actions?: readonly string[];
  /** Prompt actions resolve only against the chat a session scope names. */
  scope?: NoticeScope;
};

/** Returns all in-app actions for catalog-declared destinations on a notice. */
export function noticeActions(notice: ActionableNotice): readonly NoticeAction[] {
  if (!notice.actions || notice.actions.length === 0) {
    return [];
  }

  const result: NoticeAction[] = [];
  for (const a of notice.actions) {
    const kind = a.trim();
    if (isNoticeActionKind(kind)) {
      const act = actionFor(kind, notice.scope);
      if (act) {
        result.push(act);
      }
    }
  }
  return result;
}

/** Returns the primary in-app action for a catalog-declared destination. */
export function noticeAction(notice: ActionableNotice): NoticeAction | null {
  return noticeActions(notice)[0] ?? null;
}
